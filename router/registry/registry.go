package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/micro/go-micro/v3/logger"
	"github.com/micro/go-micro/v3/registry"
	"github.com/micro/go-micro/v3/router"
)

var (
	// RefreshInterval is the time at which we completely refresh the table
	RefreshInterval = time.Second * 120
	// PruneInterval is how often we prune the routing table
	PruneInterval = time.Second * 10
	// AdvertiseEventsTick is time interval in which the router advertises route updates
	AdvertiseEventsTick = 10 * time.Second
	// DefaultAdvertTTL is default advertisement TTL
	DefaultAdvertTTL = 2 * time.Minute
)

// rtr implements router interface
type rtr struct {
	sync.RWMutex

	running   bool
	table     *table
	options   router.Options
	exit      chan bool
	initChan  chan bool
	eventChan chan *router.Event

	// advert subscribers
	sub         sync.RWMutex
	subscribers map[string]chan *router.Advert
}

// NewRouter creates new router and returns it
func NewRouter(opts ...router.Option) router.Router {
	// get default options
	options := router.DefaultOptions()

	// apply requested options
	for _, o := range opts {
		o(&options)
	}

	// construct the router
	r := &rtr{
		options:     options,
		initChan:    make(chan bool),
		subscribers: make(map[string]chan *router.Advert),
	}

	// create the new table, passing the fetchRoute method in as a fallback if
	// the table doesn't contain the result for a query.
	r.table = newTable(r.lookup)

	// start the router
	r.start()
	return r
}

// Init initializes router with given options
func (r *rtr) Init(opts ...router.Option) error {
	r.Lock()
	for _, o := range opts {
		o(&r.options)
	}
	r.Unlock()

	// push a message to the init chan so the watchers
	// can reset in the case the registry was changed
	go func() {
		r.initChan <- true
	}()

	return nil
}

// Options returns router options
func (r *rtr) Options() router.Options {
	r.RLock()
	defer r.RUnlock()

	options := r.options

	return options
}

// Table returns routing table
func (r *rtr) Table() router.Table {
	r.Lock()
	defer r.Unlock()
	return r.table
}

func getDomain(srv *registry.Service) string {
	// check the service metadata for domain
	// TODO: domain as Domain field in registry?
	if srv.Metadata != nil && len(srv.Metadata["domain"]) > 0 {
		return srv.Metadata["domain"]
	} else if len(srv.Nodes) > 0 && srv.Nodes[0].Metadata != nil {
		return srv.Nodes[0].Metadata["domain"]
	}

	// otherwise return wildcard
	// TODO: return GlobalDomain or PublicDomain
	return registry.DefaultDomain
}

// manageRoute applies action on a given route
func (r *rtr) manageRoute(route router.Route, action string) error {
	switch action {
	case "create":
		if err := r.table.Create(route); err != nil && err != router.ErrDuplicateRoute {
			return fmt.Errorf("failed adding route for service %s: %s", route.Service, err)
		}
	case "delete":
		if err := r.table.Delete(route); err != nil && err != router.ErrRouteNotFound {
			return fmt.Errorf("failed deleting route for service %s: %s", route.Service, err)
		}
	case "update":
		if err := r.table.Update(route); err != nil {
			return fmt.Errorf("failed updating route for service %s: %s", route.Service, err)
		}
	default:
		return fmt.Errorf("failed to manage route for service %s: unknown action %s", route.Service, action)
	}

	return nil
}

// createRoutes turns a service into a list routes basically converting nodes to routes
func (r *rtr) createRoutes(service *registry.Service, network string) []router.Route {
	var routes []router.Route

	for _, node := range service.Nodes {
		routes = append(routes, router.Route{
			Service:  service.Name,
			Address:  node.Address,
			Gateway:  "",
			Network:  network,
			Router:   r.options.Id,
			Link:     router.DefaultLink,
			Metric:   router.DefaultLocalMetric,
			Metadata: node.Metadata,
		})
	}

	return routes
}

// manageServiceRoutes applies action to all routes of the service.
// It returns error of the action fails with error.
func (r *rtr) manageRoutes(service *registry.Service, action, network string) error {
	// action is the routing table action
	action = strings.ToLower(action)

	// create a set of routes from the service
	routes := r.createRoutes(service, network)

	// if its a delete action and there's no nodes
	// it means we need to wipe out all the routes
	// for that service
	if action == "delete" && len(routes) == 0 {
		// delete the service entirely
		r.table.deleteService(service.Name, network)
		return nil
	}

	// create the routes in the table
	for _, route := range routes {
		logger.Tracef("Creating route %v domain: %v", route, network)
		if err := r.manageRoute(route, action); err != nil {
			return err
		}
	}

	return nil
}

// manageRegistryRoutes applies action to all routes of each service found in the registry.
// It returns error if either the services failed to be listed or the routing table action fails.
func (r *rtr) loadRoutes(reg registry.Registry) error {
	services, err := reg.ListServices(registry.ListDomain(registry.WildcardDomain))
	if err != nil {
		return fmt.Errorf("failed listing services: %v", err)
	}

	// add each service node as a separate route
	for _, service := range services {
		// get the services domain from metadata. Fallback to wildcard.
		domain := getDomain(service)

		// create the routes
		routes := r.createRoutes(service, domain)

		// if the routes exist save them
		if len(routes) > 0 {
			logger.Tracef("Creating routes for service %v domain: %v", service, domain)
			for _, rt := range routes {
				err := r.table.Create(rt)

				// update the route to prevent it from expiring
				if err == router.ErrDuplicateRoute {
					err = r.table.Update(rt)
				}

				if err != nil {
					logger.Errorf("Error creating route for service %v in domain %v: %v", service, domain, err)
				}
			}
			continue
		}

		// otherwise get all the service info

		// get the service to retrieve all its info
		srvs, err := reg.GetService(service.Name, registry.GetDomain(domain))
		if err != nil {
			logger.Tracef("Failed to get service %s domain: %s", service.Name, domain)
			continue
		}

		// manage the routes for all returned services
		for _, srv := range srvs {
			routes := r.createRoutes(srv, domain)

			if len(routes) > 0 {
				logger.Tracef("Creating routes for service %v domain: %v", srv, domain)
				for _, rt := range routes {
					err := r.table.Create(rt)

					// update the route to prevent it from expiring
					if err == router.ErrDuplicateRoute {
						err = r.table.Update(rt)
					}

					if err != nil {
						logger.Errorf("Error creating route for service %v in domain %v: %v", service, domain, err)
					}
				}
			}
		}
	}

	return nil
}

// lookup retrieves all the routes for a given service and creates them in the routing table
func (r *rtr) lookup(service string) ([]router.Route, error) {
	logger.Tracef("Fetching route for %s domain: %v", service, registry.WildcardDomain)

	services, err := r.options.Registry.GetService(service, registry.GetDomain(registry.WildcardDomain))
	if err == registry.ErrNotFound {
		logger.Tracef("Failed to find route for %s", service)
		return nil, router.ErrRouteNotFound
	} else if err != nil {
		logger.Tracef("Failed to find route for %s: %v", service, err)
		return nil, fmt.Errorf("failed getting services: %v", err)
	}

	var routes []router.Route

	for _, srv := range services {
		domain := getDomain(srv)
		// TODO: should we continue to send the event indicating we created a route?
		// lookup is only called in the query path so probably not
		routes = append(routes, r.createRoutes(srv, domain)...)
	}

	return routes, nil
}

// watchRegistry watches registry and updates routing table based on the received events.
// It returns error if either the registry watcher fails with error or if the routing table update fails.
func (r *rtr) watchRegistry(w registry.Watcher) error {
	exit := make(chan bool)

	defer func() {
		close(exit)
	}()

	go func() {
		defer w.Stop()

		select {
		case <-exit:
			return
		case <-r.initChan:
			return
		case <-r.exit:
			return
		}
	}()

	for {
		// get the next service
		res, err := w.Next()
		if err != nil {
			if err != registry.ErrWatcherStopped {
				return err
			}
			break
		}

		// don't process nil entries
		if res.Service == nil {
			continue
		}

		// get the services domain from metadata. Fallback to wildcard.
		domain := getDomain(res.Service)

		// create/update or delete the route
		if err := r.manageRoutes(res.Service, res.Action, domain); err != nil {
			return err
		}
	}

	return nil
}

// watchTable watches routing table entries and either adds or deletes locally registered service to/from network registry
// It returns error if the locally registered services either fails to be added/deleted to/from network registry.
func (r *rtr) watchTable(w router.Watcher) error {
	exit := make(chan bool)

	defer func() {
		close(exit)
	}()

	// wait in the background for the router to stop
	// when the router stops, stop the watcher and exit
	go func() {
		defer w.Stop()

		select {
		case <-r.exit:
			return
		case <-exit:
			return
		}
	}()

	for {
		event, err := w.Next()
		if err != nil {
			if err != router.ErrWatcherStopped {
				return err
			}
			break
		}

		select {
		case <-r.exit:
			return nil
		case r.eventChan <- event:
			// process event
		}
	}

	return nil
}

// publishAdvert publishes router advert to advert channel
func (r *rtr) publishAdvert(advType router.AdvertType, events []*router.Event) {
	a := &router.Advert{
		Id:        r.options.Id,
		Type:      advType,
		TTL:       DefaultAdvertTTL,
		Timestamp: time.Now(),
		Events:    events,
	}

	r.sub.RLock()
	for _, sub := range r.subscribers {
		// now send the message
		select {
		case sub <- a:
		case <-r.exit:
			r.sub.RUnlock()
			return
		}
	}
	r.sub.RUnlock()
}

// adverts maintains a map of router adverts
type adverts map[uint64]*router.Event

// advertiseEvents advertises routing table events
// It suppresses unhealthy flapping events and advertises healthy events upstream.
func (r *rtr) advertiseEvents() error {
	// ticker to periodically scan event for advertising
	ticker := time.NewTicker(AdvertiseEventsTick)
	defer ticker.Stop()

	// adverts is a map of advert events
	adverts := make(adverts)

	// routing table watcher
	w, err := r.Watch()
	if err != nil {
		return err
	}
	defer w.Stop()

	go func() {
		var err error

		for {
			select {
			case <-r.exit:
				return
			default:
				if w == nil {
					// routing table watcher
					w, err = r.Watch()
					if err != nil {
						if logger.V(logger.DebugLevel, logger.DefaultLogger) {
							logger.Debugf("Error creating watcher: %v", err)
						}
						time.Sleep(time.Second)
						continue
					}
				}

				if err := r.watchTable(w); err != nil {
					if logger.V(logger.DebugLevel, logger.DefaultLogger) {
						logger.Debugf("Error watching table: %v", err)
					}
					time.Sleep(time.Second)
				}

				if w != nil {
					// reset
					w.Stop()
					w = nil
				}
			}
		}
	}()

	for {
		select {
		case <-ticker.C:
			// If we're not advertising any events then sip processing them entirely
			if r.options.Advertise == router.AdvertiseNone {
				continue
			}

			var events []*router.Event

			// collect all events which are not flapping
			for key, event := range adverts {
				// if we only advertise local routes skip processing anything not link local
				if r.options.Advertise == router.AdvertiseLocal && event.Route.Link != "local" {
					continue
				}

				// copy the event and append
				e := new(router.Event)
				// this is ok, because router.Event only contains builtin types
				// and no references so this creates a deep copy of struct Event
				*e = *event
				events = append(events, e)
				// delete the advert from adverts
				delete(adverts, key)
			}

			// advertise events to subscribers
			if len(events) > 0 {
				if logger.V(logger.DebugLevel, logger.DefaultLogger) {
					logger.Debugf("Router publishing %d events", len(events))
				}
				go r.publishAdvert(router.RouteUpdate, events)
			}
		case e := <-r.eventChan:
			// if event is nil, continue
			if e == nil {
				continue
			}

			// If we're not advertising any events then skip processing them entirely
			if r.options.Advertise == router.AdvertiseNone {
				continue
			}

			// if we only advertise local routes skip processing anything not link local
			if r.options.Advertise == router.AdvertiseLocal && e.Route.Link != "local" {
				continue
			}

			if logger.V(logger.DebugLevel, logger.DefaultLogger) {
				logger.Debugf("Router processing table event %s for service %s %s", e.Type, e.Route.Service, e.Route.Address)
			}

			// check if we have already registered the route
			hash := e.Route.Hash()
			ev, ok := adverts[hash]
			if !ok {
				ev = e
				adverts[hash] = e
				continue
			}

			// override the route event only if the previous event was different
			if ev.Type != e.Type {
				ev = e
			}
		case <-r.exit:
			if w != nil {
				w.Stop()
			}
			return nil
		}
	}
}

// drain all the events, only called on Stop
func (r *rtr) drain() {
	for {
		select {
		case <-r.eventChan:
		default:
			return
		}
	}
}

// start the router. Should be called under lock.
func (r *rtr) start() error {
	if r.running {
		return nil
	}

	if r.options.Precache {
		// add all local service routes into the routing table
		if err := r.loadRoutes(r.options.Registry); err != nil {
			return fmt.Errorf("failed loading registry routes: %s", err)
		}
	}

	// add default gateway into routing table
	if r.options.Gateway != "" {
		// note, the only non-default value is the gateway
		route := router.Route{
			Service: "*",
			Address: "*",
			Gateway: r.options.Gateway,
			Network: "*",
			Router:  r.options.Id,
			Link:    router.DefaultLink,
			Metric:  router.DefaultLocalMetric,
		}
		if err := r.table.Create(route); err != nil {
			return fmt.Errorf("failed adding default gateway route: %s", err)
		}
	}

	// create error and exit channels
	r.exit = make(chan bool)

	// periodically refresh all the routes
	go func() {
		t1 := time.NewTicker(RefreshInterval)
		defer t1.Stop()

		t2 := time.NewTicker(PruneInterval)
		defer t2.Stop()

		for {
			select {
			case <-r.exit:
				return
			case <-t2.C:
				r.table.pruneRoutes(RefreshInterval)
			case <-t1.C:
				if err := r.loadRoutes(r.options.Registry); err != nil {
					logger.Debugf("failed refreshing registry routes: %s", err)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-r.exit:
				return
			default:
				w, err := r.options.Registry.Watch(registry.WatchDomain(registry.WildcardDomain))
				if err != nil {
					if logger.V(logger.DebugLevel, logger.DefaultLogger) {
						logger.Debugf("failed creating registry watcher: %v", err)
					}
					time.Sleep(time.Second)
					continue
				}

				// watchRegistry calls stop when it's done
				if err := r.watchRegistry(w); err != nil {
					if logger.V(logger.DebugLevel, logger.DefaultLogger) {
						logger.Debugf("Error watching the registry: %v", err)
					}
					time.Sleep(time.Second)
				}
			}
		}
	}()

	r.running = true

	return nil
}

// Advertise stars advertising the routes to the network and returns the advertisements channel to consume from.
// If the router is already advertising it returns the channel to consume from.
// It returns error if either the router is not running or if the routing table fails to list the routes to advertise.
func (r *rtr) Advertise() (<-chan *router.Advert, error) {
	r.Lock()
	defer r.Unlock()

	// we're mutating the subscribers so they need to be locked also
	r.sub.Lock()
	defer r.sub.Unlock()

	// already advertising
	if r.eventChan != nil {
		advertChan := make(chan *router.Advert, 128)
		r.subscribers[uuid.New().String()] = advertChan
		return advertChan, nil
	}

	// list all the routes and pack them into even slice to advertise
	events, err := r.flushRouteEvents(router.Create)
	if err != nil {
		return nil, fmt.Errorf("failed to flush routes: %s", err)
	}

	// create event channels
	r.eventChan = make(chan *router.Event)

	// create advert channel
	advertChan := make(chan *router.Advert, 128)
	r.subscribers[uuid.New().String()] = advertChan

	// advertise your presence
	go r.publishAdvert(router.Announce, events)

	go func() {
		select {
		case <-r.exit:
			return
		default:
			if err := r.advertiseEvents(); err != nil {
				if logger.V(logger.DebugLevel, logger.DefaultLogger) {
					logger.Debugf("Error adveritising events: %v", err)
				}
			}
		}
	}()

	return advertChan, nil

}

// Process updates the routing table using the advertised values
func (r *rtr) Process(a *router.Advert) error {
	// NOTE: event sorting might not be necessary
	// copy update events intp new slices
	events := make([]*router.Event, len(a.Events))
	copy(events, a.Events)
	// sort events by timestamp
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	if logger.V(logger.TraceLevel, logger.DefaultLogger) {
		logger.Tracef("Router %s processing advert from: %s", r.options.Id, a.Id)
	}

	for _, event := range events {
		// skip if the router is the origin of this route
		if event.Route.Router == r.options.Id {
			if logger.V(logger.TraceLevel, logger.DefaultLogger) {
				logger.Tracef("Router skipping processing its own route: %s", r.options.Id)
			}
			continue
		}
		// create a copy of the route
		route := event.Route
		action := event.Type

		if logger.V(logger.TraceLevel, logger.DefaultLogger) {
			logger.Tracef("Router %s applying %s from router %s for service %s %s", r.options.Id, action, route.Router, route.Service, route.Address)
		}

		if err := r.manageRoute(route, action.String()); err != nil {
			return fmt.Errorf("failed applying action %s to routing table: %s", action, err)
		}
	}

	return nil
}

// flushRouteEvents returns a slice of events, one per each route in the routing table
func (r *rtr) flushRouteEvents(evType router.EventType) ([]*router.Event, error) {
	// get a list of routes for each service in our routing table
	// for the configured advertising strategy
	q := []router.QueryOption{
		router.QueryStrategy(r.options.Advertise),
	}

	routes, err := r.table.Query(q...)
	if err != nil && err != router.ErrRouteNotFound {
		return nil, err
	}

	if logger.V(logger.DebugLevel, logger.DefaultLogger) {
		logger.Debugf("Router advertising %d routes with strategy %s", len(routes), r.options.Advertise)
	}

	// build a list of events to advertise
	events := make([]*router.Event, len(routes))
	var i int

	for _, route := range routes {
		event := &router.Event{
			Type:      evType,
			Timestamp: time.Now(),
			Route:     route,
		}
		events[i] = event
		i++
	}

	return events, nil
}

// Lookup routes in the routing table
func (r *rtr) Lookup(q ...router.QueryOption) ([]router.Route, error) {
	return r.Table().Query(q...)
}

// Watch routes
func (r *rtr) Watch(opts ...router.WatchOption) (router.Watcher, error) {
	return r.table.Watch(opts...)
}

// Close the router
func (r *rtr) Close() error {
	r.Lock()
	defer r.Unlock()

	select {
	case <-r.exit:
		return nil
	default:
		if !r.running {
			return nil
		}
		close(r.exit)

		// extract the events
		r.drain()

		r.sub.Lock()
		// close advert subscribers
		for id, sub := range r.subscribers {
			// close the channel
			close(sub)
			// delete the subscriber
			delete(r.subscribers, id)
		}
		r.sub.Unlock()
	}

	// close and remove event chan
	if r.eventChan != nil {
		close(r.eventChan)
		r.eventChan = nil
	}

	r.running = false
	return nil
}

// String prints debugging information about router
func (r *rtr) String() string {
	return "registry"
}

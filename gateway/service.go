package gateway

import (
	"context"
	"fmt"
	"github.com/viant/afs"
	"github.com/viant/afs/cache"
	"github.com/viant/afs/file"
	furl "github.com/viant/afs/url"
	"github.com/viant/cloudless/resource"
	"github.com/viant/datly/auth/secret"
	"github.com/viant/datly/codec"
	"github.com/viant/datly/router"
	"github.com/viant/datly/view"
	"github.com/viant/gmetric"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

//Service represents gateway service
type Service struct {
	Config               *Config
	visitors             codec.Visitors
	types                view.Types
	mux                  sync.RWMutex
	routerResources      []*router.Resource
	routers              map[string]*router.Router
	fs                   afs.Service
	cfs                  afs.Service //cache file system
	routeResourceTracker *resource.Tracker
	dataResourceTracker  *resource.Tracker
	dataResources        map[string]*view.Resource
	metrics              *gmetric.Service
	routesReloaded       bool
}

func (r *Service) View(method, location string) (*view.View, error) {
	URI := r.Config.APIPrefix + strings.ReplaceAll(location, ".", "/")

	var err error
	var route *router.Route

	routers := r.routers

	for _, candidate := range routers {
		if route, err = candidate.Matcher.Match(method, URI); err == nil {
			return route.View, nil
		}
	}

	return nil, err
}

func (r *Service) Handle(writer http.ResponseWriter, request *http.Request) {
	err := r.handle(writer, request)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (r *Service) handle(writer http.ResponseWriter, request *http.Request) error {
	err := r.reloadResourceIfNeeded(context.Background())
	if err != nil {
		return err
	}
	URI := request.RequestURI
	if strings.Contains(URI, "://") {
		_, URI = furl.Base(URI, "https")
	}

	if request.URL == nil {
		host := os.Getenv("FUNCTION_NAME")
		if host == "" {
			host = request.Host
		}
		if host == "" {
			host = "localhost"
		}
		URL := "https://" + host + "/" + URI
		request.URL, err = url.Parse(URL)
	}
	if index := strings.Index(URI, r.Config.APIPrefix); index != -1 {
		URI = URI[index+len(r.Config.APIPrefix):]
		request.RequestURI = r.Config.APIPrefix + URI
	}
	routePath := URI
	if idx := strings.Index(URI, "?"); idx != -1 {
		routePath = URI[:idx]
	}

	aRouter, err := r.match(request.Method, routePath)
	if err == nil {
		err = aRouter.Handle(writer, request)
		if err != nil {
			err = fmt.Errorf("failed to route: %v, %v, %v %w", request.Method, request.RequestURI, request.URL.String(), err)
		}
	}
	return err
}

func (r *Service) reloadFs() afs.Service {
	if r.Config.UseCacheFS {
		return r.cfs
	}
	return r.fs
}

func (r *Service) match(method, URI string) (*router.Router, error) {
	r.mux.RLock()
	routers := r.routers
	r.mux.RUnlock()

	var err error
	for _, aRouter := range routers {
		if _, err = aRouter.Matcher.Match(method, URI); err != nil {
			return aRouter, nil
		}
	}

	var available = []string{}
	for template := range routers {
		available = append(available, r.Config.APIPrefix+template)
	}
	return nil, fmt.Errorf("%w, avail: %v", err, available)
}

func (r *Service) reloadResourceIfNeeded(ctx context.Context) error {
	if !r.routesReloaded {
		return r.reloadResource(ctx)
	}

	go func() {
		err := r.reloadResource(ctx)
		if err != nil {
			fmt.Println(fmt.Errorf("couldn't reload resource in background due to %w", err).Error())
		}
	}()

	return nil
}

func (r *Service) reloadResource(ctx context.Context) error {
	if err := r.reloadDataResourceIfNeeded(ctx); err != nil {
		log.Printf("failed to reload view reousrces: %v", err)
	}
	if err := r.reloadRouterResourcesIfNeeded(ctx); err != nil {
		return err
	}

	r.routesReloaded = true
	return nil
}

func (r *Service) reloadRouterResourcesIfNeeded(ctx context.Context) error {
	fs := r.reloadFs()
	var resourcesSnapshot = map[string]*router.Resource{}
	hasChanged := false

	err := r.routeResourceTracker.Notify(ctx, fs, r.handleRouterResourceChange(ctx, &hasChanged, resourcesSnapshot, fs))
	if err != nil || !hasChanged {
		return err
	}
	var updatedResource []*router.Resource
	routers := map[string]*router.Router{}
	for k := range resourcesSnapshot {
		item := resourcesSnapshot[k]
		for _, route := range item.Routes {
			key := r.normalize(route)
			if _, ok := routers[key]; ok {
				return fmt.Errorf("duplicate resource APIURI: %v,-> %v", key, item.SourceURL)
			}
			routers[key] = router.New(item)
		}
		updatedResource = append(updatedResource, item)
	}
	r.mux.Lock()
	defer r.mux.Unlock()
	r.routerResources = updatedResource
	r.routers = routers
	return nil
}

func (r *Service) normalize(route *router.Route) string {
	key := route.URI
	if strings.HasPrefix(key, r.Config.APIPrefix) {
		key = strings.ReplaceAll(key, r.Config.APIPrefix, "")
	}
	return key
}

func (r *Service) handleRouterResourceChange(ctx context.Context, hasChanged *bool, resourcesSnapshot map[string]*router.Resource, fs afs.Service) func(URL string, operation resource.Operation) {
	return func(URL string, operation resource.Operation) {
		if strings.Contains(URL, ".meta/") {
			return
		}
		*hasChanged = true
		if len(resourcesSnapshot) == 0 {
			r.mux.RLock()
			for i, item := range r.routerResources {
				resourcesSnapshot[item.SourceURL] = r.routerResources[i]
			}
			r.mux.RUnlock()
		}
		switch operation {
		case resource.Added, resource.Modified:
			if !strings.HasSuffix(URL, "yaml") {
				return
			}
			res, err := r.loadRouterResource(ctx, URL, fs)
			if err != nil {
				log.Printf("failed to load %v, %v\n", URL, err)
				return
			}
			resourcesSnapshot[res.SourceURL] = res
		case resource.Deleted:
			delete(resourcesSnapshot, URL)
		}
	}
}

func (r *Service) reloadDataResourceIfNeeded(ctx context.Context) error {
	fs := r.reloadFs()
	var resourcesSnapshot = map[string]*view.Resource{}
	hasChanged := false
	err := r.dataResourceTracker.Notify(ctx, fs, r.handleDataResourceChange(ctx, &hasChanged, resourcesSnapshot, fs))
	if err != nil || !hasChanged {
		return err
	}
	r.mux.Lock()
	defer r.mux.Unlock()
	r.dataResources = resourcesSnapshot
	return nil
}

func (r *Service) handleDataResourceChange(ctx context.Context, hasChanged *bool, resourcesSnapshot map[string]*view.Resource, fs afs.Service) func(URL string, operation resource.Operation) {
	return func(URL string, operation resource.Operation) {
		if strings.Contains(URL, ".meta/") {
			return
		}
		_, key := furl.Split(URL, file.Scheme)
		if index := strings.Index(key, "."); index != -1 {
			key = key[:index]
		}
		*hasChanged = true
		if len(resourcesSnapshot) == 0 {
			r.mux.RLock()
			for i, item := range r.dataResources {
				resourcesSnapshot[item.SourceURL] = r.dataResources[i]
			}
			r.mux.RUnlock()
		}
		switch operation {
		case resource.Added, resource.Modified:
			res, err := view.LoadResourceFromURL(ctx, URL, fs)
			if err != nil {
				log.Printf("failed to load %v, %v\n", URL, err)
				return
			}
			resourcesSnapshot[key] = res
		case resource.Deleted:
			delete(resourcesSnapshot, URL)
		}
	}
}

func (r *Service) loadRouterResource(ctx context.Context, URL string, fs afs.Service) (*router.Resource, error) {
	types := view.Types{}
	for k, v := range r.types {
		types[k] = v
	}

	var metrics *view.Metrics
	if r.metrics != nil {
		appURI := r.apiURI(URL)
		URIPart, _ := path.Split(appURI)
		metrics = &view.Metrics{
			Service: r.metrics,
			URIPart: URIPart,
		}
	}

	resource, err := router.NewResourceFromURL(ctx, fs, URL, r.visitors, types, r.dataResources, metrics, r.Config.Discovery())
	if err != nil {
		return nil, err
	}
	if err = r.initResource(ctx, resource, URL); err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *Service) initResource(ctx context.Context, resource *router.Resource, URL string) error {
	resource.SourceURL = URL
	if resource.APIURI == "" {
		appURI := r.apiURI(URL)
		resource.APIURI = appURI
	}
	return resource.Init(ctx)
}

func (r *Service) apiURI(URL string) string {
	path := furl.Path(r.Config.RouteURL)
	URI := URL
	index := strings.Index(URL, path)
	if index != -1 {
		URI = strings.Trim(URL[index+len(path):], "/")
	}
	if index := strings.Index(URI, "."); index != -1 {
		URI = URI[:index]
	}
	return URI
}

func (r *Service) Routes(route string) []*router.Route {
	routes := make([]*router.Route, 0)

	for _, viewRouter := range r.routers {
		routes = append(routes, viewRouter.Routes(route)...)
	}

	return routes
}

//New creates a gateway service
func New(ctx context.Context, config *Config, visitors codec.Visitors, types view.Types, metrics *gmetric.Service) (*Service, error) {
	config.Init()
	err := config.Validate()
	if err != nil {
		return nil, err
	}
	URL, _ := furl.Split(config.RouteURL, file.Scheme)
	cfs := cache.Singleton(URL)

	srv := &Service{
		visitors:             visitors,
		metrics:              metrics,
		types:                types,
		Config:               config,
		mux:                  sync.RWMutex{},
		fs:                   afs.New(),
		cfs:                  cfs,
		dataResources:        map[string]*view.Resource{},
		routeResourceTracker: resource.New(config.RouteURL, time.Duration(config.SyncFrequencyMs)*time.Millisecond),
		dataResourceTracker:  resource.New(config.DependencyURL, time.Duration(config.SyncFrequencyMs)*time.Millisecond),
	}
	if err = initSecrets(ctx, config); err != nil {
		return nil, err
	}

	err = srv.reloadResourceIfNeeded(ctx)
	return srv, err
}

func initSecrets(ctx context.Context, config *Config) error {
	if len(config.Secrets) == 0 {
		return nil
	}
	secrets := secret.New()
	for _, sec := range config.Secrets {
		if err := secrets.Apply(ctx, sec); err != nil {
			return err
		}
	}
	return nil
}

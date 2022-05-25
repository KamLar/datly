package router

import (
	"bytes"
	"context"
	goJson "encoding/json"
	"fmt"
	"github.com/viant/afs/option/content"
	"github.com/viant/datly/reader"
	"github.com/viant/datly/router/cache"
	"github.com/viant/datly/router/marshal/json"
	"github.com/viant/datly/v0/shared"
	"github.com/viant/datly/view"
	"github.com/viant/datly/visitor"
	"github.com/viant/toolbox"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"unsafe"
)

type viewHandler func(response http.ResponseWriter, request *http.Request)

const (
	AllowOriginHeader      = "Access-Control-Allow-Origin"
	AllowHeadersHeader     = "Access-Control-Allow-Headers"
	AllowMethodsHeader     = "Access-Control-Allow-Methods"
	AllowCredentialsHeader = "Access-Control-Allow-Credentials"
	ExposeHeadersHeader    = "Access-Control-Expose-Headers"
	MaxAgeHeader           = "Access-Control-Max-Age"
	Separator              = ", "
)

var errorFilters = json.NewFilters(&json.FilterEntry{
	Fields: []string{"Status", "Message"},
})
var debugEnabled = os.Getenv("DATLY_DEBUG") != ""

type Router struct {
	resource      *Resource
	serviceRouter *toolbox.ServiceRouter
}

func (r *Router) View(name string) (*view.View, error) {
	return r.resource.Resource.View(name)
}

func (r *Router) Handle(response http.ResponseWriter, request *http.Request) error {
	if err := r.serviceRouter.Route(response, request); err != nil {
		return err
	}

	return nil
}

func New(resource *Resource) *Router {
	router := &Router{
		resource: resource,
	}

	router.Init(resource.Routes)

	return router
}

func (r *Router) Init(routes Routes) {
	for _, route := range routes {
		route._resource = r.resource.Resource
	}

	r.initServiceRouter(routes)
}

func (r *Router) initServiceRouter(routes Routes) {
	routings := make([]toolbox.ServiceRouting, 0)

	for i, route := range routes {
		routings = append(routings, toolbox.ServiceRouting{
			URI:        route.URI,
			Handler:    r.viewHandler(routes[i]),
			HTTPMethod: route.Method,
			Parameters: []string{"@httpResponseWriter", "@httpRequest"},
		})

		if route.Cors != nil {
			routings = append(routings, corsRouting(route))
		}
	}

	r.serviceRouter = toolbox.NewServiceRouter(routings...)
}

func corsRouting(route *Route) toolbox.ServiceRouting {
	return toolbox.ServiceRouting{
		URI:        route.URI,
		Handler:    corsHandler(route.Cors),
		HTTPMethod: http.MethodOptions,
		Parameters: []string{"@httpResponseWriter"},
	}
}

func corsHandler(cors *Cors) func(writer http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		enableCors(writer, cors, true)
	}
}

func enableCors(writer http.ResponseWriter, cors *Cors, allHeaders bool) {
	if cors == nil {
		return
	}

	if cors.AllowOrigins != nil {
		writer.Header().Set(AllowOriginHeader, strings.Join(*cors.AllowOrigins, Separator))
	}

	if cors.AllowMethods != nil && allHeaders {
		writer.Header().Set(AllowMethodsHeader, strings.Join(*cors.AllowMethods, Separator))
	}

	if cors.AllowHeaders != nil && allHeaders {
		writer.Header().Set(AllowHeadersHeader, strings.Join(*cors.AllowHeaders, Separator))
	}

	if cors.AllowCredentials != nil && allHeaders {
		writer.Header().Set(AllowCredentialsHeader, strconv.FormatBool(*cors.AllowCredentials))
	}

	if cors.MaxAge != nil && allHeaders {
		writer.Header().Set(MaxAgeHeader, strconv.Itoa(int(*cors.MaxAge)))
	}

	if cors.ExposeHeaders != nil && allHeaders {
		writer.Header().Set(ExposeHeadersHeader, strings.Join(*cors.ExposeHeaders, Separator))
	}
}

func (r *Router) Serve(serverPath string) error {
	return http.ListenAndServe(serverPath, r)
}

func (r *Router) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	err := r.serviceRouter.Route(writer, request)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
	}
}

func (r *Router) viewHandler(route *Route) viewHandler {
	return func(response http.ResponseWriter, request *http.Request) {
		if route.Cors != nil {
			enableCors(response, route.Cors, false)
		}

		if !r.runBeforeFetch(response, request, route) {
			return
		}

		ctx := context.Background()
		selectors, err := CreateSelectorsFromRoute(ctx, route, request, route.Index._viewDetails...)
		if err != nil {
			r.writeErr(response, route, err, http.StatusBadRequest)
			return
		}

		destValue, err := r.readOrLoadFromCache(ctx, route, selectors)

		if err != nil {
			r.writeErr(response, route, err, http.StatusBadRequest)
			return
		}

		if !r.runAfterFetch(response, request, route, destValue.Interface()) {
			return
		}

		r.writeResponse(route, request, response, destValue, selectors)
	}
}

func (r *Router) readOrLoadFromCache(ctx context.Context, route *Route, selectors view.Selectors) (reflect.Value, error) {
	destValue := reflect.New(route.View.Schema.SliceType())
	dest := destValue.Interface()
	cacheEntry, err := r.cacheEntry(ctx, route, selectors)
	if err != nil {
		return destValue, err
	}

	if cacheEntry != nil && cacheEntry.Found() {
		return destValue, goJson.Unmarshal(cacheEntry.Result(), dest)
	}

	session := reader.NewSession(dest, route.View)
	session.Selectors = selectors
	if err = reader.New().Read(context.TODO(), session); err != nil {
		return destValue, err
	}

	if cacheEntry != nil {
		cacheEntry.Data = dest
		r.updateCache(ctx, route, cacheEntry)
	}

	return destValue, nil
}

func (r *Router) updateCache(ctx context.Context, route *Route, cacheEntry *cache.Entry) {
	if !debugEnabled {
		go r.putCache(ctx, route, cacheEntry)
		return
	}

	r.putCache(ctx, route, cacheEntry)
}

func (r *Router) cacheEntry(ctx context.Context, route *Route, selectors view.Selectors) (*cache.Entry, error) {
	if route.Cache == nil {
		return nil, nil
	}

	cacheEntry, err := r.createCacheEntry(route, selectors)
	if err != nil {
		return nil, err
	}

	err = route.Cache.Get(ctx, cacheEntry)
	if err != nil {
		return nil, err
	}

	return cacheEntry, nil
}

func (r *Router) putCache(ctx context.Context, route *Route, cacheEntry *cache.Entry) {
	_ = route.Cache.Put(ctx, cacheEntry)
}

func (r *Router) runBeforeFetch(response http.ResponseWriter, request *http.Request, route *Route) (shouldContinue bool) {
	if actual, ok := route.Visitor.Visitor().(visitor.BeforeFetcher); ok {
		closed, err := actual.BeforeFetch(response, request)
		if closed {
			return false
		}

		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			response.Write([]byte(err.Error()))
			return false
		}
	}
	return true
}

func (r *Router) runAfterFetch(response http.ResponseWriter, request *http.Request, route *Route, dest interface{}) (shouldContinue bool) {
	if actual, ok := route.Visitor.Visitor().(visitor.AfterFetcher); ok {
		responseClosed, err := actual.AfterFetch(dest, response, request)
		if responseClosed {
			return false
		}

		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			response.Write([]byte(err.Error()))
			return false
		}
	}

	return true
}

func (r *Router) writeResponse(route *Route, request *http.Request, response http.ResponseWriter, destValue reflect.Value, selectors view.Selectors) {
	filters, err := r.buildJsonFilters(route, selectors)
	if err != nil {
		r.writeErr(response, route, err, http.StatusBadRequest)
		return
	}

	payload, httpStatus, err := r.result(route, request, destValue, filters)
	if err != nil {
		r.writeErr(response, route, err, http.StatusBadRequest)
		return
	}

	var encoding string
	if compression := route.Compression; compression != nil && compression.MinSizeKb != 0 && len(payload) > compression.MinSizeKb*1024 {
		bytesReader := bytes.NewReader(payload)
		buffer, err := Compress(bytesReader)
		if err != nil {
			r.writeErr(response, route, err, http.StatusInternalServerError)
			return
		}
		encoding = shared.EncodingGzip
		payload = buffer.Bytes()
	}
	r.writePayload(response, payload, httpStatus, encoding)
}

func (r *Router) writePayload(response http.ResponseWriter, payload []byte, httpStatus int, encoding string) {
	response.Header().Add(content.Type, shared.ContentTypeJSON)
	response.Header().Add(content.Type, shared.CharsetUTF8)
	response.Header().Add(shared.ContentLength, strconv.Itoa(len(payload)))
	if encoding != "" {
		response.Header().Set(content.Encoding, encoding)
	}
	response.WriteHeader(httpStatus)

	response.Write(payload)
}

func (r *Router) result(route *Route, request *http.Request, destValue reflect.Value, filters *json.Filters) ([]byte, int, error) {
	if route.Cardinality == view.Many {
		result := r.wrapWithResponseIfNeeded(destValue.Elem().Interface(), route)
		asBytes, err := route._marshaller.Marshal(result, filters)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}

		return asBytes, http.StatusOK, nil
	}

	slicePtr := unsafe.Pointer(destValue.Pointer())
	sliceSize := route.View.Schema.Slice().Len(slicePtr)
	switch sliceSize {
	case 0:
		return nil, http.StatusNotFound, nil
	case 1:
		result := r.wrapWithResponseIfNeeded(route.View.Schema.Slice().ValueAt(slicePtr, 0), route)
		asBytes, err := route._marshaller.Marshal(result, filters)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}

		return asBytes, http.StatusOK, nil

	default:
		return nil, http.StatusInternalServerError, fmt.Errorf("for route %v expected query to return zero or one result but returned %v", request.RequestURI, sliceSize)
	}
}

func (r *Router) buildJsonFilters(route *Route, selectors view.Selectors) (*json.Filters, error) {
	entries := make([]*json.FilterEntry, 0)
	for viewName, selector := range selectors {
		if len(selector.Columns) == 0 {
			continue
		}

		var path string
		viewByName, ok := route.Index.viewByName(viewName)
		if !ok {
			path = ""
		} else {
			path = viewByName.Path
		}

		fields := make([]string, len(selector.Fields))
		for i := range selector.Fields {
			fields[i] = selector.Fields[i]
		}

		entries = append(entries, &json.FilterEntry{
			Path:   path,
			Fields: fields,
		})

	}

	return json.NewFilters(entries...), nil
}

func (r *Router) writeErr(w http.ResponseWriter, route *Route, err error, statusCode int) {
	if route._responseSetter == nil {
		w.WriteHeader(statusCode)
		w.Write([]byte(err.Error()))
		return
	}

	response := reflect.New(route._responseSetter.rType)
	r.setResponseStatus(route, response, ResponseStatus{
		Status:  "error",
		Message: err.Error(),
	})

	asBytes, marErr := route._marshaller.Marshal(response.Elem().Interface(), errorFilters)
	if marErr != nil {
		w.Write([]byte(marErr.Error()))
		w.WriteHeader(statusCode)
		return
	}

	w.Write(asBytes)
	w.WriteHeader(statusCode)
}

func (r *Router) setResponseStatus(route *Route, response reflect.Value, responseStatus ResponseStatus) {
	route._responseSetter.statusField.SetValue(unsafe.Pointer(response.Pointer()), responseStatus)
}

func (r *Router) wrapWithResponseIfNeeded(response interface{}, route *Route) interface{} {
	if route._responseSetter == nil {
		return response
	}

	newResponse := reflect.New(route._responseSetter.rType)
	route._responseSetter.bodyField.SetValue(unsafe.Pointer(newResponse.Pointer()), response)
	r.setResponseStatus(route, newResponse, ResponseStatus{Status: "ok"})
	return newResponse.Elem().Interface()
}

func (r *Router) createCacheEntry(route *Route, selectors view.Selectors) (*cache.Entry, error) {
	selectorSlice := make([]*view.Selector, len(selectors))
	for viewName := range selectors {
		index, _ := route.viewIndex(viewName)
		selectorSlice[index] = selectors[viewName]
	}

	marshalled, err := goJson.Marshal(selectorSlice)
	if err != nil {
		return nil, err
	}

	return &cache.Entry{
		View:      route.View,
		Selectors: marshalled,
	}, nil
}

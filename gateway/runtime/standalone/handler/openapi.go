package handler

import (
	"github.com/viant/datly/router"
	"github.com/viant/datly/router/openapi3"
	"gopkg.in/yaml.v3"
	"net/http"
	"strings"
)

type (
	OpenAPI struct {
		APIPrefix string
		baseURL   string
		routesFn  RoutesFn
		info      openapi3.Info
	}

	RoutesFn func(route string) []*router.Route
)

func (o *OpenAPI) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	URI := req.RequestURI
	if index := strings.Index(URI, o.baseURL); index != -1 {
		URI = URI[index+len(o.baseURL):]
	}

	var routeURL string
	if URI != "" {
		routeURL = o.APIPrefix + URI
	}
	routes := o.routesFn(routeURL)
	if len(routes) == 0 {
		writer.WriteHeader(http.StatusNotFound)
		return
	}

	spec, err := router.GenerateOpenAPI3Spec(o.info, routes...)
	if err != nil {
		writer.Write([]byte(err.Error()))
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}

	specMarshal, err := yaml.Marshal(spec)
	if err != nil {
		writer.Write([]byte(err.Error()))
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/yaml")
	writer.Write(specMarshal)
	writer.WriteHeader(http.StatusOK)
}

func NewOpenApi(aPIPrefix, baseURL string, info openapi3.Info, routesFn RoutesFn) *OpenAPI {
	return &OpenAPI{
		APIPrefix: aPIPrefix,
		routesFn:  routesFn,
		info:      info,
		baseURL:   baseURL,
	}
}

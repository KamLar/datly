package standalone

import (
	"context"
	"fmt"
	"github.com/viant/datly/gateway"
	"github.com/viant/datly/gateway/registry"
	"github.com/viant/datly/gateway/runtime/standalone/handler"
	"github.com/viant/gmetric"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"
)

type Server struct {
	http.Server
}

//shutdownOnInterrupt server on interupts
func (r *Server) shutdownOnInterrupt() {
	closed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint
		// We received an interrupt signal, shut down.
		if err := r.Shutdown(context.Background()); err != nil {
			// Error from closing listeners, or context timeout:
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(closed)
	}()
}

func New(config *Config) (*Server, error) {
	config.Init()
	mux := http.NewServeMux()
	metric := gmetric.New()
	if config.Config == nil {
		return nil, fmt.Errorf("gateway config was empty")
	}
	service, err := gateway.SingletonWithConfig(config.Config, registry.Codecs, registry.Types, metric)
	if err != nil {
		return nil, err
	}
	mux.Handle(config.Meta.MetricURI, gmetric.NewHandler(config.Meta.MetricURI, metric))
	mux.Handle(config.Meta.ConfigURI, handler.NewConfig(config.Config, &config.Endpoint, &config.Meta))
	mux.Handle(config.Meta.StatusURI, handler.NewStatus(config.Version, &config.Meta))
	mux.Handle(config.Meta.ViewURI, handler.NewView(config.Meta.ViewURI, &config.Meta, service.View))

	//actual datly handler
	mux.HandleFunc(config.Config.APIPrefix, service.Handle)
	server := &Server{
		Server: http.Server{
			Addr:           ":" + strconv.Itoa(config.Endpoint.Port),
			Handler:        mux,
			ReadTimeout:    time.Millisecond * time.Duration(config.Endpoint.ReadTimeoutMs),
			WriteTimeout:   time.Millisecond * time.Duration(config.Endpoint.WriteTimeoutMs),
			MaxHeaderBytes: config.Endpoint.MaxHeaderBytes,
		},
	}
	server.shutdownOnInterrupt()
	return server, nil
}
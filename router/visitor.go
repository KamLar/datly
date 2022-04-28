package router

import (
	"fmt"
	"net/http"
)

type (
	//LifecycleVisitor visitor can implement BeforeFetcher and/or AfterFetcher
	LifecycleVisitor interface{}

	//BeforeFetcher represents Lifecycle hook which is called before data was read from the Database.
	BeforeFetcher interface {
		//BeforeFetch one of the lifecycle hooks, returns bool if response was closed (i.e. response.WriteHeader())
		//or just an error if it is needed to stop the router flow.
		BeforeFetch(response http.ResponseWriter, request *http.Request) (responseClosed bool, err error)
	}

	//AfterFetcher represents Lifecycle hook with is called after data was read from Database.
	//It is important to specify View type for assertion type purposes.
	AfterFetcher interface {

		//AfterFetch one of the lifecycle hooks, returns bool if response was closed (i.e. response.WriteHeader())
		//or just an error if it is needed to stop the router flow.
		//data is type of *[]T or *[]*T
		AfterFetch(data interface{}, response http.ResponseWriter, request *http.Request) (responseClosed bool, err error)
	}
)

type Visitors map[string]*Visitor

func (v Visitors) Lookup(name string) (*Visitor, error) {
	visitor, ok := v[name]
	if !ok {
		return nil, fmt.Errorf("not found visitor with name %v", name)
	}

	return visitor, nil
}

func (v Visitors) Register(visitor *Visitor) {
	v[visitor.Name] = visitor
}

func NewVisitors(visitors ...*Visitor) Visitors {
	result := Visitors{}
	for i := range visitors {
		result.Register(visitors[i])
	}

	return result
}

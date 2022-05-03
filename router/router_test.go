package router_test

import (
	"context"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/viant/assertly"
	"github.com/viant/datly/data"
	_ "github.com/viant/sqlx/metadata/product/sqlite"
	"io/ioutil"
	"math"
	"net/http/httptest"
	"reflect"
	"strconv"
	"time"

	"github.com/viant/datly/router"
	"github.com/viant/dsunit"
	"github.com/viant/toolbox"
	"net/http"
	"path"
	"testing"
)

type testcase struct {
	description string
	resourceURI string
	uri         string
	method      string
	expected    string
	visitors    router.Visitors
	types       data.Types
}

type (
	eventAfterFetcher  struct{}
	eventBeforeFetcher struct{}
)

func (e *eventBeforeFetcher) BeforeFetch(response http.ResponseWriter, request *http.Request) (responseClosed bool, err error) {
	response.Write([]byte("[]"))
	response.WriteHeader(http.StatusBadRequest)

	return true, nil
}

func (e *eventAfterFetcher) AfterFetch(data interface{}, response http.ResponseWriter, request *http.Request) (responseClosed bool, err error) {
	if _, ok := data.(*[]*event); !ok {
		response.Write([]byte("unexpected data type"))
		response.WriteHeader(http.StatusInternalServerError)
		return true, nil
	}

	events := data.(*[]*event)
	for _, ev := range *events {
		ev.Quantity = math.Round(ev.Quantity)
	}

	return false, nil
}

type event struct {
	Id        int
	Quantity  float64
	Timestamp time.Time
}

//TODO: add testcases against sql injection
func TestRouter(t *testing.T) {
	testLocation := toolbox.CallerDirectory(3)

	testcases := []*testcase{
		{
			description: "regular http",
			resourceURI: "001_get",
			uri:         "/api/events",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
		},
		{
			description: "selectors | fields, offset, limit",
			resourceURI: "002_selectors",
			uri:         fmt.Sprintf("/api/events?%v=ev.Id|ev.Quantity&%v=ev.1&%v=2", router.Fields, router.Offset, router.Limit),
			expected:    `[{"Id":10,"Quantity":21.957962334156036},{"Id":100,"Quantity":5.084940046072006}]`,
			method:      http.MethodGet,
		},
		{
			description: "selectors | orderBy, offset, limit",
			resourceURI: "002_selectors",
			uri:         fmt.Sprintf("/api/events?%v=ev.Quantity&%v=ev.1&%v=ev.3", router.OrderBy, router.Offset, router.Limit),
			expected:    `[{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}]`,
			method:      http.MethodGet,
		},
		{
			description: "selectors | orderBy, criteria",
			resourceURI: "002_selectors",
			uri:         fmt.Sprintf("/api/events?%v=Id&%v=ev.ID%%20in%%20(1,100)", router.OrderBy, router.Criteria),
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:      http.MethodGet,
		},
		{
			description: "return single | found",
			resourceURI: "003_route_config",
			uri:         "/api/events/1",
			expected:    `{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}`,
			method:      http.MethodGet,
		},
		{
			description: "return single | not found",
			resourceURI: "003_route_config",
			uri:         "/api/events/3",
			method:      http.MethodGet,
		},
		{
			description: "visitors | AfterFetcher",
			resourceURI: "004_visitors",
			uri:         "/api/events",
			visitors: router.NewVisitors(
				router.NewVisitor("event_visitor", &eventAfterFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Quantity":33,"Timestamp":"2019-03-11T02:20:33Z"},{"Id":10,"Quantity":22,"Timestamp":"2019-03-15T12:07:33Z"},{"Id":100,"Quantity":5,"Timestamp":"2019-04-10T05:15:33Z"}]`,
			method:   http.MethodGet,
		},
		{
			description: "visitors | BeforeFetcher",
			resourceURI: "004_visitors",
			uri:         "/api/events",
			visitors: router.NewVisitors(
				router.NewVisitor("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | all values set",
			resourceURI: "004_visitors",
			uri:         "/api/events",
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			visitors: router.NewVisitors(
				router.NewVisitor("event_visitor", &eventBeforeFetcher{}),
			),
			expected: `[]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | none value set",
			resourceURI: "005_templates",
			uri:         "/api/events",
			visitors: router.NewVisitors(
				router.NewVisitor("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | user_id set",
			resourceURI: "005_templates",
			uri:         "/api/events?user_id=1",
			visitors: router.NewVisitors(
				router.NewVisitor("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}]`,
			method:   http.MethodGet,
		},
	}

	//for i, testcase := range testcases[len(testcases)-1:] {
	for i, testcase := range testcases {
		fmt.Println("Running testcase " + strconv.Itoa(i))
		testUri := path.Join(testLocation, "testdata")
		routingHandler, ok := testcase.init(t, testUri)
		if !ok {
			continue
		}

		httpRequest := httptest.NewRequest(testcase.method, testcase.uri, nil)
		responseWriter := httptest.NewRecorder()
		err := routingHandler.Handle(responseWriter, httpRequest)
		if !assert.Nil(t, err, testcase.description) {
			continue
		}

		response, err := ioutil.ReadAll(responseWriter.Result().Body)
		if !assert.Nil(t, err, testcase.description) {
			continue
		}

		if !assertly.AssertValues(t, testcase.expected, string(response), testcase.description) {
			fmt.Println(string(response))
		}
	}
}

func (c *testcase) init(t *testing.T, testDataLocation string) (*router.Router, bool) {
	resourceURI := path.Join(testDataLocation, c.resourceURI)

	if !initDb(t, testDataLocation, c.resourceURI) {
		return nil, false
	}

	resource, err := router.NewResourceFromURL(context.TODO(), path.Join(resourceURI, "resource.yaml"), c.visitors, c.types)
	if !assert.Nil(t, err, c.description) {
		return nil, false
	}

	return router.New(resource), true
}

func initDb(t *testing.T, datasetPath string, resourceURI string) bool {
	configPath := path.Join(datasetPath, "db_config.yaml")
	if !dsunit.InitFromURL(t, configPath) {
		return false
	}

	datasetURI := path.Join(datasetPath, resourceURI, "populate")
	initDataset := dsunit.NewDatasetResource("db", datasetURI, "", "")
	request := dsunit.NewPrepareRequest(initDataset)
	if !dsunit.Prepare(t, request) {
		return false
	}

	return true
}

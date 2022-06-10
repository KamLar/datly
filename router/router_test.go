package router_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/golang-jwt/jwt/v4"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/viant/afs"
	"github.com/viant/afs/option/content"
	"github.com/viant/assertly"
	"github.com/viant/datly/codec"
	"github.com/viant/datly/gateway/registry"
	"github.com/viant/datly/view"
	_ "github.com/viant/sqlx/metadata/product/sqlite"
	"google.golang.org/api/oauth2/v2"
	"io"
	"io/ioutil"
	"math"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/viant/datly/router"
	"github.com/viant/dsunit"
	"github.com/viant/toolbox"
	"net/http"
	"path"
	"testing"
)

type testcase struct {
	description      string
	resourceURI      string
	uri              string
	method           string
	expected         string
	visitors         codec.Visitors
	types            view.Types
	headers          http.Header
	requestBody      string
	shouldDecompress bool
	extraRequests    int
	envVariables     map[string]string

	corsHeaders     map[string]string
	dependenciesUrl map[string]string
}

type (
	eventAfterFetcher  struct{}
	eventBeforeFetcher struct{}
	gcpMockDecoder     struct{}
)

func (g *gcpMockDecoder) Value(_ context.Context, raw string, _ ...interface{}) (interface{}, error) {
	tokenType := "Bearer "
	if index := strings.Index(raw, tokenType); index != -1 {
		raw = raw[index+len(tokenType):]
		decoded, err := base64.URLEncoding.DecodeString(raw)
		if err != nil {
			return nil, err
		}

		claims := jwt.MapClaims{}
		_, _ = jwt.ParseWithClaims(string(decoded), claims, func(token *jwt.Token) (interface{}, error) {
			return nil, nil
		})

		email := claims["Email"]
		if emailAsString, ok := email.(string); ok {
			return &oauth2.Tokeninfo{
				Email: emailAsString,
			}, nil
		}

		return &oauth2.Tokeninfo{}, err
	}

	return nil, fmt.Errorf("unsupported token type")
}

func (e *eventBeforeFetcher) BeforeFetch(response http.ResponseWriter, request *http.Request) (responseClosed bool, err error) {
	response.WriteHeader(http.StatusBadRequest)
	response.Write([]byte("[]"))
	return true, nil
}

func (e *eventAfterFetcher) AfterFetch(data interface{}, response http.ResponseWriter, request *http.Request) (responseClosed bool, err error) {
	if _, ok := data.(*[]*event); !ok {
		response.WriteHeader(http.StatusInternalServerError)
		response.Write([]byte("unexpected data type"))
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

type (
	filtersSchemaType struct {
		Columns []Filter `json:"column"`
	}

	Filter struct {
		SearchValues []int  `json:"search_values"`
		Column       string `json:"column_name"`
		Inclusive    bool   `json:"inclusive"`
		Radius       int    `json:"radius"`
	}
)

//TODO: add testcases against sql injection
func TestRouter(t *testing.T) {
	testLocation := toolbox.CallerDirectory(3)
	_ = toolbox.CreateDirIfNotExist(path.Join(testLocation, "testdata/db"))

	type FooParam struct {
		QUANTITY float64
		USER_ID  int
	}

	type params struct {
		FOO           *FooParam
		EVENT_TYPE_ID int
	}

	type FooPresence struct {
		QUANTITY bool
		USER_ID  bool
	}

	type presenceParams struct {
		FOO           *FooPresence
		EVENT_TYPE_ID bool
	}

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
			uri:         fmt.Sprintf("/api/events?ev%v=Id,Quantity&ev%v=1&%v=2", router.Fields, router.Offset, router.Limit),
			expected:    `[{"Id":10,"Quantity":21.957962334156036},{"Id":100,"Quantity":5.084940046072006}]`,
			method:      http.MethodGet,
		},
		{
			description: "selectors | orderBy, offset, limit",
			resourceURI: "002_selectors",
			uri:         fmt.Sprintf("/api/events?ev%v=Quantity&ev%v=1&ev%v=3", router.OrderBy, router.Offset, router.Limit),
			expected:    `[{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}]`,
			method:      http.MethodGet,
		},
		{
			description: "selectors | orderBy, criteria",
			resourceURI: "002_selectors",
			uri:         fmt.Sprintf("/api/events?%v=Id&ev%v=(ID%%20in%%20(1,100))", router.OrderBy, router.Criteria),
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
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventAfterFetcher{}),
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
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventBeforeFetcher{}),
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
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventBeforeFetcher{}),
			),
			expected: `[]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | none value set",
			resourceURI: "005_templates",
			uri:         "/api/events",
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | user_id",
			resourceURI: "005_templates",
			uri:         "/api/events?user_id=1",
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}]`,
			method:   http.MethodGet,
		},
		{
			description: "templates | quantity",
			resourceURI: "005_templates",
			uri:         "/api/events?quantity=10",
			visitors: codec.NewVisitors(
				codec.New("event_visitor", &eventBeforeFetcher{}),
			),
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2}]`,
			method:   http.MethodGet,
		},
		{
			description: "param path | all set",
			resourceURI: "006_param_path",
			uri:         "/api/events?quantity=10&event_type_id=2&user_id=1",
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1}]`,
			method:   http.MethodGet,
		},
		{
			description: "param path | user_id",
			resourceURI: "006_param_path",
			uri:         "/api/events?user_id=3",
			types: map[string]reflect.Type{
				"event": reflect.TypeOf(&event{}),
			},
			expected: `[{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:   http.MethodGet,
		},
		{
			description: "param path typed | user_id, non-pointers",
			resourceURI: "007_param_path_typed",
			uri:         "/api/events?user_id=3",
			types: map[string]reflect.Type{
				"event":           reflect.TypeOf(&event{}),
				"params":          reflect.TypeOf(params{}),
				"presence_params": reflect.TypeOf(presenceParams{}),
			},
			expected: `[{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:   http.MethodGet,
		},
		{
			description: "param path typed | user_id, pointers",
			resourceURI: "007_param_path_typed",
			uri:         "/api/events?user_id=3",
			types: map[string]reflect.Type{
				"event":           reflect.TypeOf(&event{}),
				"params":          reflect.TypeOf(&params{}),
				"presence_params": reflect.TypeOf(&presenceParams{}),
			},
			expected: `[{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:   http.MethodGet,
		},
		{
			description: "view acl | role leader",
			resourceURI: "008_acl",
			uri:         "/api/employees",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"DepId":1,"Email":"abc@example.com"},{"Id":3,"DepId":1,"Email":"tom@example.com"}]`,
			headers: map[string][]string{
				//ID: 1, Email: abc@example.com
				"Authorization": {"Bearer " + encodeToken("eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJJZCI6MSwiRW1haWwiOiJhYmNAZXhhbXBsZS5jb20ifQ.dm3jSSuqy9wf4BsjU1dElRQQEySC5nn6fCUTmTKqt2")},
			},
			visitors: codec.NewVisitors(
				codec.New(registry.CodecKeyJwtClaim, &gcpMockDecoder{}),
			),
			types: map[string]reflect.Type{
				registry.TypeJwtTokenInfo: reflect.TypeOf(&oauth2.Tokeninfo{}),
			},
		},
		{
			description: "view acl | role engineer",
			resourceURI: "008_acl",
			uri:         "/api/employees",
			method:      http.MethodGet,
			expected:    `[{"Id":2,"DepId":2,"Email":"example@gmail.com"}]`,
			headers: map[string][]string{
				//ID: 1
				"Authorization": {"Bearer " + encodeToken("eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJJZCI6MiwiRW1haWwiOiJleGFtcGxlQGdtYWlsLmNvbSJ9.XsZ115KqQK8uQE9for6NaphYS1VHdJc_famKWHo1Dcw")},
			},
			visitors: codec.NewVisitors(
				codec.New(registry.CodecKeyJwtClaim, &gcpMockDecoder{}),
			),
			types: map[string]reflect.Type{
				registry.TypeJwtTokenInfo: reflect.TypeOf(&oauth2.Tokeninfo{}),
			},
		},
		{
			description: "view acl | user acl",
			resourceURI: "008_acl",
			uri:         "/api/employees",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"DepId":1,"Email":"abc@example.com"},{"Id":2,"DepId":2,"Email":"example@gmail.com"},{"Id":3,"DepId":1,"Email":"tom@example.com"},{"Id":4,"DepId":2,"Email":"Ann@example.com"}]`,
			headers: map[string][]string{
				//ID: 4
				"Authorization": {"Bearer " + encodeToken("eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJFbWFpbCI6IkFubkBleGFtcGxlLmNvbSIsIklkIjo0fQ.9A0LWtsh_tskG-hLBFVNj7PNRQE8qWc5ZioqLWPS1gQ")},
			},
			visitors: codec.NewVisitors(
				codec.New(registry.CodecKeyJwtClaim, &gcpMockDecoder{}),
			),
			types: map[string]reflect.Type{
				registry.TypeJwtTokenInfo: reflect.TypeOf(&oauth2.Tokeninfo{}),
			},
		},
		{
			description: "CORS",
			resourceURI: "009_cors",
			uri:         "/api/events",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			corsHeaders: map[string]string{
				router.AllowCredentialsHeader: "true",
				router.AllowHeadersHeader:     "Header-1, Header-2",
				router.AllowOriginHeader:      "*",
				router.ExposeHeadersHeader:    "Header-Exposed-1, Header-Exposed-2",
				router.MaxAgeHeader:           "10500",
				router.AllowMethodsHeader:     "POST, PATCH",
			},
		},
		{
			description: "relations | with specified fields",
			resourceURI: "010_relations",
			uri:         fmt.Sprintf("/api/events?ev%v=Id,Quantity,EventType&typ%v=Id,Code&ev%v=1&%v=2", router.Fields, router.Fields, router.Offset, router.Limit),
			method:      http.MethodGet,
			expected:    `[{"Id":10,"Quantity":21.957962334156036,"EventType":{"Id":11,"Code":"code - 11"}},{"Id":100,"Quantity":5.084940046072006,"EventType":{"Id":111,"Code":"code - 111"}}]`,
		},
		{
			description: "relations | with specified fields, without relation Id",
			resourceURI: "010_relations",
			expected:    `[{"Id":10,"Quantity":21.957962334156036,"EventType":{"Code":"code - 11"}},{"Id":100,"Quantity":5.084940046072006,"EventType":{"Code":"code - 111"}}]`,
			uri:         fmt.Sprintf("/api/events?ev%v=Id,Quantity,EventType&typ%v=Code&ev%v=1&%v=2", router.Fields, router.Fields, router.Offset, router.Limit),
			method:      http.MethodGet,
		},
		{
			description: "relations | with specified fields, without relation",
			resourceURI: "010_relations",
			expected:    `[{"Id":10,"Quantity":21.957962334156036},{"Id":100,"Quantity":5.084940046072006}]`,
			uri:         fmt.Sprintf("/api/events?ev%v=Id,Quantity&ev%v=1&%v=2", router.Fields, router.Offset, router.Limit),
			method:      http.MethodGet,
		},
		{
			description: "styles | error",
			resourceURI: "011_style",
			uri:         "/api/events?_criteria=(id%20=%201%20UNION%20ALL%20SELECT%209%20as%20id%2C%20To_Date%28%222019-03-11T02%3A20%3A33Z%22%29%20as%20timestamp%2C%2010%20as%20event_type_id%2C%2020%20as%20quantity%2C%206%20as%20user_id)",
			expected:    `{"Status":"error","Message":"can't use criteria on view events"}`,
			method:      http.MethodGet,
		},
		{
			description: "styles | response",
			resourceURI: "011_style",
			uri:         "/api/events?_fields=Id,Timestamp,EventTypeId",
			expected:    `{"Status":"ok","Result":[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111}]}`,
			method:      http.MethodGet,
		},
		{
			description: "default | default tag",
			resourceURI: "012_default",
			uri:         "/api/events",
			expected:    `[{"Id":1,"Quantity":33.23432374000549,"Timestamp":"2019-03-11"},{"Id":10,"Quantity":21.957962334156036,"Timestamp":"2019-03-15"},{"Id":100,"Quantity":10.5,"Timestamp":"2019-04-10"}]`,
			method:      http.MethodGet,
		},
		{
			description:      "reader post | request body param",
			resourceURI:      "013_reader_post",
			uri:              "/api/events",
			expected:         `[{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2}]`,
			method:           http.MethodPost,
			requestBody:      `{"UserId":2,"Id":10}`,
			shouldDecompress: true,
		},
		{
			description:      "reader post | compressed",
			resourceURI:      "013_reader_post",
			uri:              "/api/events",
			expected:         `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			method:           http.MethodPost,
			shouldDecompress: true,
		},
		{
			description:   "cache | fields, offset, limit",
			resourceURI:   "014_cache",
			uri:           fmt.Sprintf("/api/events?ev%v=Id,Quantity&ev%v=1&%v=2", router.Fields, router.Offset, router.Limit),
			expected:      `[{"Id":10,"Quantity":21.957962334156036},{"Id":100,"Quantity":5.084940046072006}]`,
			method:        http.MethodGet,
			extraRequests: 1,
		},
		{
			description: "relations | with specified fields, without relation Id",
			resourceURI: "015_relations_many",
			expected:    `[{"Id":1,"Events":[]},{"Id":2,"Events":[{"UserId":1},{"UserId":10}]},{"Id":11,"Events":[{"UserId":2}]},{"Id":111,"Events":[{"UserId":3}]}]`,
			uri:         "/api/event-types?_fields=Events,Id&ev_fields=UserId",
			method:      http.MethodGet,
		},
		{
			description: "case format | with specified fields",
			resourceURI: "016_case_format",
			expected:    `[{"id":1,"events":[]},{"id":2,"events":[{"userId":1},{"userId":10}]},{"id":11,"events":[{"userId":2}]},{"id":111,"events":[{"userId":3}]}]`,
			uri:         "/api/event-types?_fields=events,id&ev_fields=userId",
			method:      http.MethodGet,
		},
		{
			description: "case format | criteria",
			resourceURI: "016_case_format",
			expected:    `[{"id":1,"events":[]},{"id":2,"events":[{"id":123,"timestamp":"2019-04-10T05:15:33Z","quantity":5,"userId":10}]},{"id":11,"events":[{"id":10,"timestamp":"2019-03-15T12:07:33Z","quantity":21.957962334156036,"userId":2}]},{"id":111,"events":[{"id":100,"timestamp":"2019-04-10T05:15:33Z","quantity":5.084940046072006,"userId":3}]}]`,
			//(userId in (10,2,3))
			uri:    "/api/event-types?_fields=events,id&ev_criteria=%28userId%20in%20%2810%2C2%2C3%29%29",
			method: http.MethodGet,
		},
		{
			description: "time_logger ",
			resourceURI: "017_time_logger",
			uri:         "/api/events",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
		},
		{
			description: "relations_template | dicover criteria",
			resourceURI: "018_relations_template",
			uri:         "/api/events?eventTypeId=2",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","Quantity":33.23432374000549,"UserId":1,"EventType":{"Id":2,"Type":"type - 2","Code":"code - 2"}},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","Quantity":21.957962334156036,"UserId":2,"EventType":null},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","Quantity":5.084940046072006,"UserId":3,"EventType":null}]`,
		},
		{
			description: "custom selector | environment variables",
			resourceURI: "019_custom_selector",
			uri:         "/api/events",
			method:      http.MethodGet,
			expected:    `[{"Id":1,"Timestamp":"2019-03-11T02:20:33Z","EventTypeId":2,"Quantity":33.23432374000549,"UserId":1},{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2},{"Id":100,"Timestamp":"2019-04-10T05:15:33Z","EventTypeId":111,"Quantity":5.084940046072006,"UserId":3}]`,
			envVariables: map[string]string{
				"alias": "t",
				"table": "events",
			},
		},
		{
			description: "custom selector | custom selector params",
			resourceURI: "019_custom_selector",
			uri:         "/api/events?limit=2&skip=1&names=Id,Quantity&sort=Quantity",
			method:      http.MethodGet,
			envVariables: map[string]string{
				"alias": "t",
				"table": "events",
			},
			expected: `[{"Id":10,"Quantity":21.957962334156036},{"Id":1,"Quantity":33.23432374000549}]`,
		},
		{
			description: "slices | filters",
			resourceURI: "020_slices",
			uri:         "/api/events?filters=%7B%22column%22:%5B%7B%22column_name%22:%22user_id%22,%22search_values%22:%5B2,11%5D,%22inclusive%22:true%7D,%7B%22column_name%22:%22event_type_id%22,%22search_values%22:%5B2,11%5D,%22inclusive%22:true%7D%5D%7D",
			method:      http.MethodGet,
			envVariables: map[string]string{
				"alias": "t",
				"table": "events",
			},
			expected: `[{"Id":10,"Timestamp":"2019-03-15T12:07:33Z","EventTypeId":11,"Quantity":21.957962334156036,"UserId":2}]`,
		},
	}

	//for i, tCase := range testcases[len(testcases)-1:] {
	for i, tCase := range testcases {
		if i != 0 {
			testcases[i-1].cleanup()
		}

		//for i, tCase := range testcases {
		fmt.Println("Running testcase " + strconv.Itoa(i))
		testUri := path.Join(testLocation, "testdata")
		routingHandler, ok := tCase.init(t, testUri)
		if !ok {
			continue
		}

		if tCase.corsHeaders != nil {
			corsRequest := httptest.NewRequest(http.MethodOptions, tCase.uri, nil)
			corsWriter := httptest.NewRecorder()
			err := routingHandler.Handle(corsWriter, corsRequest)
			assert.Nil(t, err, tCase.description)

			headers := corsWriter.Header()
			for headerName, headerValue := range tCase.corsHeaders {
				assert.Equal(t, headerValue, headers.Get(headerName), tCase.description)
			}
		}

		for j := 0; j < tCase.extraRequests+1; j++ {
			if !tCase.sendHttpRequest(t, routingHandler) {
				return
			}
		}
	}
}

func (c *testcase) init(t *testing.T, testDataLocation string) (*router.Router, bool) {
	for name, value := range c.envVariables {
		os.Setenv(name, value)
	}

	resourceURI := path.Join(testDataLocation, c.resourceURI)
	fs := afs.New()
	if !initDb(t, testDataLocation, c.resourceURI) {
		return nil, false
	}

	dependencies := map[string]*view.Resource{}
	for name, URL := range c.dependenciesUrl {
		resourceUrl := path.Join(resourceURI, fmt.Sprintf("%v.yaml", URL))
		resource, ok := c.readViewResource(t, resourceUrl, c.types, c.visitors)
		if !ok {
			return nil, false
		}
		dependencies[name] = resource
	}

	resourceUrl := path.Join(resourceURI, "resource.yaml")
	resource, ok := c.readResource(t, fs, resourceUrl, dependencies)
	if !ok {
		return nil, false
	}

	return router.New(resource), true
}

func (c *testcase) readResource(t *testing.T, fs afs.Service, resourceUrl string, dependencies map[string]*view.Resource) (*router.Resource, bool) {
	resource, err := router.NewResourceFromURL(context.TODO(), fs, resourceUrl, c.visitors, c.types, dependencies, nil)
	if !assert.Nil(t, err, c.description) {
		return nil, false
	}
	return resource, true
}

func (c *testcase) readViewResource(t *testing.T, resourceUrl string, types view.Types, visitors codec.Visitors) (*view.Resource, bool) {
	resource, err := view.NewResourceFromURL(context.TODO(), resourceUrl, types, visitors)
	if !assert.Nil(t, err, c.description) {
		return nil, false
	}
	return resource, true
}

func (c *testcase) sendHttpRequest(t *testing.T, handler *router.Router) bool {
	var body io.Reader
	if c.method != http.MethodGet {
		body = bytes.NewReader([]byte(c.requestBody))
	}
	httpRequest := httptest.NewRequest(c.method, c.uri, body)
	for header, values := range c.headers {
		httpRequest.Header.Add(header, values[0])
	}

	responseWriter := httptest.NewRecorder()
	err := handler.Handle(responseWriter, httpRequest)
	if !assert.Nil(t, err, c.description) {
		return false
	}

	response, err := ioutil.ReadAll(responseWriter.Result().Body)
	if !assert.Nil(t, err, c.description) {
		return false
	}

	if c.shouldDecompress {
		assert.Equal(t, router.EncodingGzip, responseWriter.Header().Get(content.Encoding), c.description)
		reader, err := gzip.NewReader(bytes.NewReader(response))
		assert.Nil(t, err, c.description)
		decompressed, err := ioutil.ReadAll(reader)
		assert.Nil(t, err, c.description)
		response = decompressed
	}

	if !assertly.AssertValues(t, c.expected, string(response), c.description) {
		fmt.Println(string(response))
	}

	return true
}

func (c *testcase) cleanup() {
	for key := range c.envVariables {
		os.Unsetenv(key)
	}
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

func encodeToken(token string) string {
	return base64.StdEncoding.EncodeToString([]byte(token))
}

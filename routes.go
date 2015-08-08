package extdirect
import (
	"net/http"
	"fmt"
	"strings"
	"io"
	"io/ioutil"
	"encoding/json"
	"reflect"
	"github.com/mitchellh/mapstructure"
	"github.com/zenazn/goji/web"
)

type ErrInvalidContentType string
func (this ErrInvalidContentType) Error() string {
	return fmt.Sprintf("invalid content type: %s", string(this))
}

type ErrTypeConversion struct {
	SourceType reflect.Type
	TargetType reflect.Type
}
func (this *ErrTypeConversion) Error() string {
	return fmt.Sprintf("cannot convert type %v to type %v", this.SourceType, this.TargetType)
}

type ErrDirectActionMethod struct {
	Action string
	Method string
	Err    interface{}
}
func (this *ErrDirectActionMethod) Error() string {
	return fmt.Sprintf("error serving %v.%v: %v", this.Action, this.Method, this.Err)
}

type request struct {
	Type   string `json:"type"`
	Tid    int `json:"tid"`
	Action string `json:"action"`
	Method string `json:"method"`
	Upload bool `json:"upload"`
	Data   interface{} `json:"data"`
}

type response struct {
	Type    string `json:"type"`
	Tid     int `json:"tid"`
	Action  string `json:"action"`
	Method  string `json:"method"`
	Message string `json:"message"`
	Result  interface{} `json:"result"`
}

func Api(provider *DirectServiceProvider) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		if js, err := provider.JavaScript(); err != nil {
			panic(err)
		} else {
			if _, err := w.Write([]byte(js)); err != nil {
				panic(err)
			}
		}
	}
}

func ActionsHandler(provider *DirectServiceProvider) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		actionHandler(provider, nil, w, r)
	}
}

func ActionsHandlerCtx(provider *DirectServiceProvider) func(c web.C, w http.ResponseWriter, r *http.Request) {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		actionHandler(provider, &c, w, r)
	}
}

func actionHandler(provider *DirectServiceProvider, c *web.C, w http.ResponseWriter, r *http.Request) {
	var reqs []*request
	contentType := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		reqs = mustDecodeTransaction(r.Body)
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
	// httpReq.ParseForm()
	// reqs = this.decodeFormPost(httpReq.Form)
	default:
		panic(ErrInvalidContentType(contentType))
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(provider.processRequests(c, r, reqs)); err != nil {
		panic(err)
	}
}

func (this *DirectServiceProvider) processRequests(c *web.C, r *http.Request, reqs []*request) []*response {
	resps := make([]*response, len(reqs))
	respsChannel := make(chan *response, len(reqs))
	for _, req := range reqs {
		go func(req *request) {
			resp := &response{
				Tid: req.Tid,
				Action: req.Action,
				Method: req.Method,
				Type: req.Type,
			}
			defer func() {
				if err := recover(); err != nil {
					log.Print(&ErrDirectActionMethod{req.Action, req.Method, err})
					resp.Type = "exception"
					resp.Message = fmt.Sprintf("%v", err)
				}
				respsChannel <- resp
			}()

			// Create instance of action type
			if this.writeLog {
				log.Print(fmt.Sprintf("Create instance of action %s", req.Action))
			}
			actionInfo := this.actionsInfo[req.Action]
			actionVal := reflect.New(actionInfo.Type).Elem()

			// Set context and request
			if c != nil || r != nil {
				if this.writeLog {
					log.Print("Set action context/request.")
				}
				contextType := reflect.TypeOf(&web.C{})
				requestType := reflect.TypeOf(&http.Request{})
				fieldsLen := actionInfo.Type.NumField()
				for i := 0; i < fieldsLen; i++ {
					switch actionInfo.Type.Field(i).Type {
					case contextType:
						if c != nil {
							if this.writeLog {
								log.Print("Set action context.")
							}
							actionVal.Field(i).Set(reflect.ValueOf(c))
						}
					case requestType:
						if r != nil {
							if this.writeLog {
								log.Print("Set action request.")
							}
							actionVal.Field(i).Set(reflect.ValueOf(r))
						}
					}
				}
			}

			// Call action method
			if this.writeLog {
				log.Print(fmt.Sprintf("Prepare arguments for method %s.%s", req.Action, req.Method))
			}
			methodInfo := actionInfo.Methods[req.Method]
			methodArgsLen := methodInfo.Type.NumIn() - 1
			var args []reflect.Value
			if req.Data != nil {
				args = make([]reflect.Value, methodArgsLen)
				for i, arg := range req.Data.([]interface{}) {
					if this.writeLog {
						log.Print(fmt.Sprintf("Initial arg #%v type is %T", i, arg))
					}
					convertedArg := convertArg(methodInfo.Type.In(i + 1), arg)
					if this.writeLog {
						log.Print(fmt.Sprintf("Converted arg #%v type is %T", i, convertedArg))
					}
					args[i] = reflect.ValueOf(convertedArg)
				}
			}
			if this.writeLog {
				log.Print(fmt.Sprintf("Call method %s.%s", req.Action, req.Method))
			}
			resultsValues := actionVal.MethodByName(methodInfo.Name).Call(args)
			for i, resultValue := range resultsValues {
				if methodInfo.Type.Out(i).Name() == "error" {
					if err, isErr := resultValue.Interface().(error); isErr {
						log.Print(&ErrDirectActionMethod{req.Action, req.Method, err})
						resp.Type = "exception"
						resp.Message = fmt.Sprintf("%v", err)
						resp.Result = nil
						break;
					}
				} else {
					result := resultValue.Interface()
					resp.Result = result
				}
			}
		}(req)
	}

	for i := 0; i< len(reqs); i++ {
		var resp = <-respsChannel
		resps[i] = resp
	}

	return resps
}

func mustDecodeTransaction(r io.Reader) []*request {
	if jsonData, err := ioutil.ReadAll(r); err != nil {
		panic(err)
	} else {
		var reqs []*request
		if err := json.Unmarshal(jsonData, &reqs); err != nil {
			var req request
			if err := json.Unmarshal(jsonData, &req); err != nil {
				panic(err)
			} else {
				reqs = make([]*request, 1)
				reqs[0] = &req
			}
		}
		return reqs
	}
}

func convertArg(argType reflect.Type, argValue interface{}) interface{} {
	sourceType := reflect.TypeOf(argValue)
	if sourceType != argType {
		switch v := argValue.(type) {
		case float64:
			switch argType.Kind() {
			case reflect.Int: return int(v)
			case reflect.Int8: return int8(v)
			case reflect.Int16: return int16(v)
			case reflect.Int32: return int32(v)
			case reflect.Float32: return float32(v)
			default: panic(&ErrTypeConversion{sourceType, argType})
			}
		case nil:
			switch argType.Kind() {
			case reflect.Int: return int(0)
			case reflect.Int8: return int8(0)
			case reflect.Int16: return int16(0)
			case reflect.Int32: return int32(0)
			case reflect.String: return ""
			default: panic(&ErrTypeConversion{sourceType, argType})
			}
		case map[string]interface{}:
			switch argType.Kind() {
			case reflect.Ptr: fallthrough
			case reflect.Struct:
				structInstanceValue := reflect.New(argType).Elem()
				structInstanceRef := structInstanceValue.Addr().Interface()
				mapstructure.Decode(v, structInstanceRef)
				return structInstanceValue.Interface()
			default: panic(&ErrTypeConversion{sourceType, argType})
			}
		}
	}

	return argValue
}
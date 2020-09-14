package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"github.com/gocql/gocql"
    "math/big"
    "gopkg.in/inf.v0"
    "strconv"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

// newDatasource returns datasource.ServeOpts.
func newDatasource() datasource.ServeOpts {
	// creates a instance manager for your plugin. The function passed
	// into `NewInstanceManger` is called when the instance is created
	// for the first time or when a datasource configuration changed.
	im := datasource.NewInstanceManager(newDataSourceInstance)
	ds := &SampleDatasource{
		im: im,
	}

	return datasource.ServeOpts{
		QueryDataHandler:   ds,
		CheckHealthHandler: ds,
	}
}

// SampleDatasource is an example datasource used to scaffold
// new datasource plugins with an backend.
type SampleDatasource struct {
	// The instance manager can help with lifecycle management
	// of datasource instances in plugins. It's not a requirements
	// but a best practice that we recommend that you follow.
	im instancemgmt.InstanceManager
}

// QueryData handles multiple queries and returns multiple responses.
// req contains the queries []DataQuery (where each query contains RefID as a unique identifer).
// The QueryDataResponse contains a map of RefID to the response for each query, and each response
// contains Frames ([]*Frame).
func (td *SampleDatasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	log.DefaultLogger.Info("QueryData", "request", req)

	// create response struct
	response := backend.NewQueryDataResponse()

    instance, err := td.im.Get(req.PluginContext)
    if err != nil {
        return nil, err
    }
    instSetting, _ := instance.(*instanceSettings)

	// loop over queries and execute them individually.
	for _, q := range req.Queries {
		res := td.query(ctx, q, instSetting)

		// save the response in a hashmap
		// based on with RefID as identifier
		response.Responses[q.RefID] = res
	}

	return response, nil
}
func getTypeArray(typ string) interface{} {
    log.DefaultLogger.Debug("getTypeArray", "type", typ)
    switch t := typ; t {
        case "timestamp":
            return []time.Time{}
        case "bigint", "int":
            return []int64{}
        case "smallint":
            return []int16{}
        case "boolean":
            return []bool{}
        case "double", "varint", "decimal":
            return []float64{}
        case "float":
            return []float32{}
        case "tinyint":
            return []int8{}
        default:
            return []string{}
    }
}

func toValue(val interface{}, typ string) interface{} {
    if (val == nil) {
        return nil
    }
    switch t := val.(type) {
        case float32, time.Time, string, int64, float64, bool, int16, int8:
            return t
        case gocql.UUID:
            return t.String()
        case int:
            return int64(t)
        case *inf.Dec:
            if s, err := strconv.ParseFloat(t.String(), 64); err == nil {
                return s
            }
            return 0
        case *big.Int:
            if s, err := strconv.ParseFloat(t.String(), 64); err == nil {
                return s
            }
            return 0
        default:
            r, err := json.Marshal(val)
            if (err != nil) {
                log.DefaultLogger.Info("Marshalling failed ", "err", err)
            }
            return string(r)
    }
}

type queryModel struct {
	Format string `json:"format"`
	QueryText string `json:"queryText"`
	Host string `json:"host"`
}

func (td *SampleDatasource) query(ctx context.Context, query backend.DataQuery, instSetting *instanceSettings) backend.DataResponse {
	// Unmarshal the json into our queryModel
	var qm queryModel

	response := backend.DataResponse{}

	response.Error = json.Unmarshal(query.JSON, &qm)
	if response.Error != nil {
		return response
	}
    log.DefaultLogger.Info("Getting query information", "query", qm.QueryText, "host", qm.Host)
	// Log a warning if `Format` is empty.
	if qm.Format == "" {
		log.DefaultLogger.Warn("format is empty. defaulting to time series")
	}

	// create data frame response
	frame := data.NewFrame("response")
    iter := instSetting.session.Query(qm.QueryText).Iter()

    for _, c := range iter.Columns() {
        log.DefaultLogger.Info("Adding Column", "name", c.Name, "type", c.TypeInfo.Type().String())
        frame.Fields = append(frame.Fields,
            data.NewField(c.Name, nil, getTypeArray(c.TypeInfo.Type().String())),
        )
    }
    for {
        // New map each iteration
        row := make(map[string]interface{})
        if !iter.MapScan(row) {
            break
        }
        vals := make([]interface{}, len(iter.Columns()))
        for i, c := range iter.Columns() {
            vals[i] = toValue(row[c.Name], c.TypeInfo.Type().String())
        }
        frame.AppendRow(vals...)
    }
    if err := iter.Close(); err != nil {
        log.DefaultLogger.Warn(err.Error())
    }
	// add the frames to the response
	response.Frames = append(response.Frames, frame)

	return response
}

// CheckHealth handles health checks sent from Grafana to the plugin.
// The main use case for these health checks is the test button on the
// datasource configuration page which allows users to verify that
// a datasource is working as expected.
func (td *SampleDatasource) CheckHealth(ctx context.Context, req *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	var status = backend.HealthStatusOk
	var message = "Data source is working"

	if rand.Int()%2 == 0 {
		status = backend.HealthStatusError
		message = "randomized error"
	}

	return &backend.CheckHealthResult{
		Status:  status,
		Message: message,
	}, nil
}

type instanceSettings struct {
	cluster *gocql.ClusterConfig
	session *gocql.Session
}

func newDataSourceInstance(setting backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
    type editModel struct {
        Host string `json:"host"`
    }
    var hosts editModel
    err := json.Unmarshal(setting.JSONData, &hosts)
    if err != nil {
        log.DefaultLogger.Warn("error marshalling", "err", err)
        return nil, err
    }
    var secureData = setting.DecryptedSecureJSONData
    password, hasPassword := secureData["password"]
    user, hasUser := secureData["user"]
    var authenticator *gocql.PasswordAuthenticator = nil
    if hasPassword && hasUser {
        log.DefaultLogger.Info("Adding authenticator for user", "user", user)
        authenticator = &gocql.PasswordAuthenticator{
            Username: user,
            Password: password,
        }
    }
    log.DefaultLogger.Info("looking for host", "host", hosts.Host)
    var newCluster *gocql.ClusterConfig = nil
    newCluster = gocql.NewCluster(hosts.Host)
    if authenticator != nil {
        newCluster.Authenticator = *authenticator
    }
    session, _ := gocql.NewSession(*newCluster)
	return &instanceSettings{
		cluster: newCluster,
		session: session,
	}, nil
}

func (s *instanceSettings) Dispose() {
	// Called before creatinga a new instance to allow plugin authors
	// to cleanup.
}

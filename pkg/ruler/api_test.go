package ruler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/weaveworks/common/user"
	"github.com/weaveworks/cortex/pkg/configs"
	"github.com/weaveworks/cortex/pkg/configs/db"
	"github.com/weaveworks/cortex/pkg/configs/db/dbtest"
)

const (
	endpoint = "/api/prom/rules"
)

var (
	app      *API
	database db.DB
	counter  int
)

// setup sets up the environment for the tests.
func setup(t *testing.T) {
	database = dbtest.Setup(t)
	app = NewAPI(database)
	counter = 0
}

// cleanup cleans up the environment after a test.
func cleanup(t *testing.T) {
	dbtest.Cleanup(t, database)
}

// request makes a request to the configs API.
func request(t *testing.T, method, urlStr string, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r, err := http.NewRequest(method, urlStr, body)
	require.NoError(t, err)
	app.ServeHTTP(w, r)
	return w
}

// requestAsUser makes a request to the configs API as the given user.
func requestAsUser(t *testing.T, userID string, method, urlStr string, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r, err := http.NewRequest(method, urlStr, body)
	require.NoError(t, err)
	r = r.WithContext(user.InjectOrgID(r.Context(), userID))
	user.InjectOrgIDIntoHTTPRequest(r.Context(), r)
	app.ServeHTTP(w, r)
	return w
}

// makeString makes a string, guaranteed to be unique within a test.
func makeString(pattern string) string {
	counter++
	return fmt.Sprintf(pattern, counter)
}

// makeUserID makes an arbitrary user ID. Guaranteed to be unique within a test.
func makeUserID() string {
	return makeString("user%d")
}

// makeRulerConfig makes an arbitrary ruler config
func makeRulerConfig() configs.RulesConfig {
	return configs.RulesConfig(map[string]string{
		"filename.rules": makeString(`
# Config no. %d.
ALERT ScrapeFailed
  IF          up != 1
  FOR         10m
  LABELS      { severity="warning" }
  ANNOTATIONS {
    summary = "Scrape of {{$labels.job}} (pod: {{$labels.instance}}) failed.",
    description = "Prometheus cannot reach the /metrics page on the {{$labels.instance}} pod.",
    impact = "We have no monitoring data for {{$labels.job}} - {{$labels.instance}}. At worst, it's completely down. At best, we cannot reliably respond to operational issues.",
    dashboardURL = "$${base_url}/admin/prometheus/targets",
  }
		`),
	})
}

// parseVersionedRulesConfig parses a configs.VersionedRulesConfig from JSON.
func parseVersionedRulesConfig(t *testing.T, b []byte) configs.VersionedRulesConfig {
	var x configs.VersionedRulesConfig
	err := json.Unmarshal(b, &x)
	require.NoError(t, err, "Could not unmarshal JSON: %v", string(b))
	return x
}

// post a config
func post(t *testing.T, userID string, oldConfig configs.RulesConfig, newConfig configs.RulesConfig) configs.VersionedRulesConfig {
	updateRequest := configUpdateRequest{
		OldConfig: oldConfig,
		NewConfig: newConfig,
	}
	b, err := json.Marshal(updateRequest)
	require.NoError(t, err)
	reader := bytes.NewReader(b)
	w := requestAsUser(t, userID, "POST", endpoint, reader)
	require.Equal(t, http.StatusNoContent, w.Code)
	return get(t, userID)
}

// get a config
func get(t *testing.T, userID string) configs.VersionedRulesConfig {
	w := requestAsUser(t, userID, "GET", endpoint, nil)
	return parseVersionedRulesConfig(t, w.Body.Bytes())
}

// configs returns 404 if no config has been created yet.
func Test_GetConfig_NotFound(t *testing.T) {
	setup(t)
	defer cleanup(t)

	userID := makeUserID()
	w := requestAsUser(t, userID, "GET", endpoint, nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// configs returns 401 to requests without authentication.
func Test_PostConfig_Anonymous(t *testing.T) {
	setup(t)
	defer cleanup(t)

	w := request(t, "POST", endpoint, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// Posting to a configuration sets it so that you can get it again.
func Test_PostConfig_CreatesConfig(t *testing.T) {
	setup(t)
	defer cleanup(t)

	userID := makeUserID()
	config := makeRulerConfig()
	result := post(t, userID, nil, config)
	assert.Equal(t, config, result.Config)
}

// Posting to a configuration sets it so that you can get it again.
func Test_PostConfig_UpdatesConfig(t *testing.T) {
	setup(t)
	defer cleanup(t)

	userID := makeUserID()
	config1 := makeRulerConfig()
	view1 := post(t, userID, nil, config1)
	config2 := makeRulerConfig()
	view2 := post(t, userID, config1, config2)
	assert.True(t, view2.ID > view1.ID, "%v > %v", view2.ID, view1.ID)
	assert.Equal(t, config2, view2.Config)
}

// Different users can have different configurations.
func Test_PostConfig_MultipleUsers(t *testing.T) {
	setup(t)
	defer cleanup(t)

	userID1 := makeUserID()
	userID2 := makeUserID()
	config1 := post(t, userID1, nil, makeRulerConfig())
	config2 := post(t, userID2, nil, makeRulerConfig())
	foundConfig1 := get(t, userID1)
	assert.Equal(t, config1, foundConfig1)
	foundConfig2 := get(t, userID2)
	assert.Equal(t, config2, foundConfig2)
	assert.True(t, config2.ID > config1.ID, "%v > %v", config2.ID, config1.ID)
}

// // GetAllConfigs returns an empty list of configs if there aren't any.
// func Test_GetAllConfigs_Empty(t *testing.T) {
// 	setup(t)
// 	defer cleanup(t)

// 	w := request(t, "GET", c.PrivateEndpoint, nil)
// 	assert.Equal(t, http.StatusOK, w.Code)
// 	var found api.ConfigsView
// 	err := json.Unmarshal(w.Body.Bytes(), &found)
// 	assert.NoError(t, err, "Could not unmarshal JSON")
// 	assert.Equal(t, api.ConfigsView{Configs: map[string]configs.View{}}, found)
// }

// // GetAllConfigs returns all created configs.
// func Test_GetAllConfigs(t *testing.T) {
// 	setup(t)
// 	defer cleanup(t)

// 	userID := makeUserID()
// 	config := makeRulerConfig()

// 	view := c.post(t, userID, config)
// 	w := request(t, "GET", c.PrivateEndpoint, nil)
// 	assert.Equal(t, http.StatusOK, w.Code)
// 	var found api.ConfigsView
// 	err := json.Unmarshal(w.Body.Bytes(), &found)
// 	assert.NoError(t, err, "Could not unmarshal JSON")
// 	assert.Equal(t, api.ConfigsView{Configs: map[string]configs.View{
// 		userID: view,
// 	}}, found)
// }

// // GetAllConfigs returns the *newest* versions of all created configs.
// func Test_GetAllConfigs_Newest(t *testing.T) {
// 	setup(t)
// 	defer cleanup(t)

// 	userID := makeUserID()

// 	c.post(t, userID, makeRulerConfig())
// 	c.post(t, userID, makeRulerConfig())
// 	lastCreated := c.post(t, userID, makeRulerConfig())

// 	w := request(t, "GET", c.PrivateEndpoint, nil)
// 	assert.Equal(t, http.StatusOK, w.Code)
// 	var found api.ConfigsView
// 	err := json.Unmarshal(w.Body.Bytes(), &found)
// 	assert.NoError(t, err, "Could not unmarshal JSON")
// 	assert.Equal(t, api.ConfigsView{Configs: map[string]configs.View{
// 		userID: lastCreated,
// 	}}, found)
// }

// func Test_GetConfigs_IncludesNewerConfigsAndExcludesOlder(t *testing.T) {
// 	setup(t)
// 	defer cleanup(t)

// 	c.post(t, makeUserID(), makeRulerConfig())
// 	config2 := c.post(t, makeUserID(), makeRulerConfig())
// 	userID3 := makeUserID()
// 	config3 := c.post(t, userID3, makeRulerConfig())

// 	w := request(t, "GET", fmt.Sprintf("%s?since=%d", c.PrivateEndpoint, config2.ID), nil)
// 	assert.Equal(t, http.StatusOK, w.Code)
// 	var found api.ConfigsView
// 	err := json.Unmarshal(w.Body.Bytes(), &found)
// 	assert.NoError(t, err, "Could not unmarshal JSON")
// 	assert.Equal(t, api.ConfigsView{Configs: map[string]configs.View{
// 		userID3: config3,
// 	}}, found)
// }

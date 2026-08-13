package planner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/weihesdlegend/Vacation-planner/test/redis_client_mocks"
	"github.com/weihesdlegend/Vacation-planner/user"
)

func TestCreateTokenSlidingInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &MyPlanner{RedisClient: redis_client_mocks.RedisClient}
	router := gin.New()
	router.POST("/v1/create-token", p.createNewPAT)

	userView, err := redis_client_mocks.RedisClient.CreateUser(
		redis_client_mocks.RedisContext,
		user.View{Username: "sliding_create_svc", Email: "sliding_create_svc@example.com", Password: "pwd", UserLevel: user.LevelStringRegular},
		false,
	)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	pat, err := redis_client_mocks.RedisClient.NewPAT(
		redis_client_mocks.RedisContext, "sliding-create-auth", userView.ID, "sliding-create-auth-token", time.Hour, 0,
	)
	if err != nil {
		t.Fatalf("failed to create auth PAT: %v", err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/create-token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pat.TokenHash)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("sliding interval accepted", func(t *testing.T) {
		w := post(`{"name": "svc-sliding", "sliding_interval": "720h"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["renewInterval"] != "720h0m0s" {
			t.Errorf("expected renewInterval 720h0m0s, got %v", resp["renewInterval"])
		}
		if resp["token"] == "" {
			t.Error("expected token in response")
		}
	})

	t.Run("mutually exclusive with expiration_duration", func(t *testing.T) {
		w := post(`{"name": "svc-both", "sliding_interval": "720h", "expiration_duration": "24h"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid format rejected", func(t *testing.T) {
		w := post(`{"name": "svc-bad", "sliding_interval": "30 days"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("below minimum rejected", func(t *testing.T) {
		w := post(`{"name": "svc-short", "sliding_interval": "30m"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("above maximum rejected", func(t *testing.T) {
		w := post(`{"name": "svc-long", "sliding_interval": "8761h"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("fixed expiration still works without renewInterval in response", func(t *testing.T) {
		w := post(`{"name": "svc-fixed", "expiration_duration": "24h"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if _, ok := resp["renewInterval"]; ok {
			t.Error("fixed token must not include renewInterval")
		}
	})
}

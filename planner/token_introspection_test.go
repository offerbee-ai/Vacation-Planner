package planner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/weihesdlegend/Vacation-planner/test/redis_client_mocks"
	"github.com/weihesdlegend/Vacation-planner/user"
)

func TestGetTokenIntrospection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &MyPlanner{RedisClient: redis_client_mocks.RedisClient}
	router := gin.New()
	router.GET("/v1/token", p.getTokenInfo)

	userView, err := redis_client_mocks.RedisClient.CreateUser(
		redis_client_mocks.RedisContext,
		user.View{Username: "introspect_svc", Email: "introspect_svc@example.com", Password: "pwd", UserLevel: user.LevelStringRegular},
		false,
	)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	get := func(authorization string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/token", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("no header", func(t *testing.T) {
		if w := get(""); w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 without credentials, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		if w := get("Bearer not-a-real-token"); w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 with invalid token, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("fixed token returns metadata without renewInterval", func(t *testing.T) {
		pat, err := redis_client_mocks.RedisClient.NewPAT(
			redis_client_mocks.RedisContext, "introspect-fixed", userView.ID, "introspect-fixed-token", time.Hour, 0,
		)
		if err != nil {
			t.Fatalf("failed to create PAT: %v", err)
		}

		w := get("Bearer " + pat.TokenHash)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["name"] != "introspect-fixed" {
			t.Errorf("expected name introspect-fixed, got %v", resp["name"])
		}
		if resp["valid"] != true {
			t.Errorf("expected valid true, got %v", resp["valid"])
		}
		if _, err := time.Parse(time.RFC3339, resp["expiresAt"].(string)); err != nil {
			t.Errorf("expiresAt not RFC3339: %v", resp["expiresAt"])
		}
		if _, ok := resp["renewInterval"]; ok {
			t.Error("fixed token must not include renewInterval")
		}
	})

	t.Run("sliding token past halfway slides on introspection", func(t *testing.T) {
		// 20m left of a 1h window: introspection itself should renew it.
		_, err := redis_client_mocks.RedisClient.NewPAT(
			redis_client_mocks.RedisContext, "introspect-sliding", userView.ID, "introspect-sliding-token", 20*time.Minute, time.Hour,
		)
		if err != nil {
			t.Fatalf("failed to create PAT: %v", err)
		}

		w := get("Bearer introspect-sliding-token")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %v", err)
		}
		if resp["renewInterval"] != "1h0m0s" {
			t.Errorf("expected renewInterval 1h0m0s, got %v", resp["renewInterval"])
		}
		expiresAt, err := time.Parse(time.RFC3339, resp["expiresAt"].(string))
		if err != nil {
			t.Fatalf("expiresAt not RFC3339: %v", resp["expiresAt"])
		}
		if time.Until(expiresAt) <= 55*time.Minute {
			t.Errorf("expected expiry slid to ~now+1h, got %v away", time.Until(expiresAt))
		}
	})
}

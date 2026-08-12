package planner

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthzReturnsOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	p := &MyPlanner{}
	p.healthz(c)

	if recorder.Code != 200 {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("expected body 'ok', got %q", recorder.Body.String())
	}
}

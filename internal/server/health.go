package server

import (
	"net/http"

	"github.com/Prog-Strength/prog-strength-api/internal/httpresp"
)

func HealthCheck(w http.ResponseWriter, r *http.Request) {
	httpresp.OK(w, "service is healthy", nil)
}

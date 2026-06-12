package nutritionlookup

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth/authctx"
)

var errAlwaysDown = errors.New("provider down")

// lookupEnvelope mirrors the httpresp success shape with the lookup
// result typed so handler tests can assert on matches.
type lookupEnvelope struct {
	Message string `json:"message"`
	Data    Result `json:"data"`
}

type errEnvelope struct {
	Error string `json:"error"`
}

// getLookup drives GET /nutrition/lookup through a mounted router with
// userID-in-context, mirroring the nutrition handler test pattern.
func getLookup(t *testing.T, svc *Service, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	NewHandler(svc).Mount(r)
	req := httptest.NewRequest("GET", "/nutrition/lookup?"+rawQuery, nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// happyService returns a service whose single fake provider serves one
// candidate.
func happyService() (*Service, *fakeProvider) {
	fs := &fakeProvider{
		source:     "fatsecret",
		configured: true,
		hits: []Candidate{newCandidate(
			"Chick-n-Minis (4 Count)", "Chick-fil-A", "4 minis",
			Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41},
			"fatsecret", "12345",
		)},
	}
	return NewService(NewMemoryRepository(), fs), fs
}

func TestLookupParamValidation(t *testing.T) {
	tests := []struct {
		name     string
		rawQuery string
		wantMsg  string
	}{
		{"missing query", "quantity=2", "query is required"},
		{"blank query", "query=%20%20", "query is required"},
		{"query too long", "query=" + strings.Repeat("a", 201), "query is too long"},
		{"quantity not a number", "query=eggs&quantity=lots", "quantity must be a positive number"},
		{"quantity zero", "query=eggs&quantity=0", "quantity must be a positive number"},
		{"quantity negative", "query=eggs&quantity=-2", "quantity must be a positive number"},
		{"max_results not an int", "query=eggs&max_results=many", "max_results must be an integer between 1 and 10"},
		{"max_results zero", "query=eggs&max_results=0", "max_results must be an integer between 1 and 10"},
		{"max_results too big", "query=eggs&max_results=11", "max_results must be an integer between 1 and 10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fs := happyService()
			w := getLookup(t, svc, tt.rawQuery)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			var env errEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if !strings.Contains(env.Error, tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", env.Error, tt.wantMsg)
			}
			if fs.calls != 0 {
				t.Errorf("provider called %d times on a 400, want 0", fs.calls)
			}
		})
	}
}

func TestLookupSuccessEnvelope(t *testing.T) {
	svc, _ := happyService()
	w := getLookup(t, svc, "query=chicken+minis&quantity=2&max_results=3")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var env lookupEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Message != "nutrition lookup results" {
		t.Errorf("message = %q, want %q", env.Message, "nutrition lookup results")
	}
	if env.Data.Quantity != 2 {
		t.Errorf("quantity = %v, want 2", env.Data.Quantity)
	}
	if len(env.Data.Matches) != 1 {
		t.Fatalf("len(matches) = %d, want 1", len(env.Data.Matches))
	}
	match := env.Data.Matches[0]
	if match.Name != "Chick-n-Minis (4 Count)" {
		t.Errorf("name = %q", match.Name)
	}
	if match.TotalForQuantity.Calories != 720 {
		t.Errorf("total calories = %v, want 720 (scaled in Go, not by the model)", match.TotalForQuantity.Calories)
	}
}

func TestLookupDefaultsQuantityAndMaxResults(t *testing.T) {
	svc, fs := happyService()
	w := getLookup(t, svc, "query=chicken+minis")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var env lookupEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Data.Quantity != 1 {
		t.Errorf("quantity = %v, want default 1", env.Data.Quantity)
	}
	if fs.calls != 1 {
		t.Errorf("fs.calls = %d, want 1", fs.calls)
	}
}

func TestLookupUnavailableWhenNoProvidersConfigured(t *testing.T) {
	svc := NewService(
		NewMemoryRepository(),
		&fakeProvider{source: "fatsecret"},
		&fakeProvider{source: "usda"},
	)
	w := getLookup(t, svc, "query=eggs")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", w.Code, w.Body.String())
	}
	var env errEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error != "lookup_unavailable: no nutrition data providers configured" {
		t.Errorf("error = %q", env.Error)
	}
}

func TestLookupFailedWhenAllProvidersDownAndNoCache(t *testing.T) {
	svc := NewService(
		NewMemoryRepository(),
		&fakeProvider{source: "fatsecret", configured: true, err: errAlwaysDown},
		&fakeProvider{source: "usda", configured: true, err: errAlwaysDown},
	)
	w := getLookup(t, svc, "query=eggs")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", w.Code, w.Body.String())
	}
	var env errEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if !strings.HasPrefix(env.Error, "lookup_failed: ") {
		t.Errorf("error = %q, want a lookup_failed prefix", env.Error)
	}
	if !strings.Contains(env.Error, "fatsecret") || !strings.Contains(env.Error, "usda") {
		t.Errorf("error = %q, want both provider names in the detail", env.Error)
	}
}

func TestLookupMissingUserInContextIs500(t *testing.T) {
	svc, _ := happyService()
	r := chi.NewRouter()
	NewHandler(svc).Mount(r)
	req := httptest.NewRequest("GET", "/nutrition/lookup?query=eggs", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when auth middleware was not applied", w.Code)
	}
}

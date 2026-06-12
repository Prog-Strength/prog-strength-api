package nutritionlookup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fsTokenJSON = `{"access_token": "tok-1", "token_type": "Bearer", "expires_in": 86400}`

// fsFoodJSON renders one foods.search hit with the documented
// food_description format.
func fsFoodJSON(foodID, name, brand, description string) string {
	out, _ := json.Marshal(map[string]string{
		"food_id":          foodID,
		"food_name":        name,
		"food_type":        "Brand",
		"brand_name":       brand,
		"food_description": description,
	})
	return string(out)
}

const fsMinisDescription = "Per 4 minis - Calories: 360kcal | Fat: 13.00g | Carbs: 41.00g | Protein: 19.00g"

func TestFatSecretSearchParsesDescriptionAndSendsBearer(t *testing.T) {
	var gotAuth, gotMethod, gotExpr string

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token request method = %s, want POST", r.Method)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "id" || pass != "secret" {
			t.Errorf("token request basic auth = (%q, %q, %v), want (id, secret, true)", user, pass, ok)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		if got := r.PostForm.Get("scope"); got != "basic" {
			t.Errorf("scope = %q, want basic", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fsTokenJSON)
	}))
	t.Cleanup(tokenSrv.Close)
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.URL.Query().Get("method")
		gotExpr = r.URL.Query().Get("search_expression")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"foods": {"food": [%s]}}`,
			fsFoodJSON("12345", "Chick-n-Minis (4 Count)", "Chick-fil-A", fsMinisDescription))
	}))
	t.Cleanup(searchSrv.Close)

	p := NewFatSecretProvider(searchSrv.Client(), "id", "secret", testLogger())
	p.TokenURL = tokenSrv.URL
	p.APIURL = searchSrv.URL

	hits, err := p.Search(context.Background(), "chick fil a chicken minis", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer tok-1")
	}
	if gotMethod != "foods.search" {
		t.Errorf("method param = %q, want foods.search", gotMethod)
	}
	if gotExpr != "chick fil a chicken minis" {
		t.Errorf("search_expression = %q, want the query", gotExpr)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	hit := hits[0]
	if hit.Name != "Chick-n-Minis (4 Count)" {
		t.Errorf("Name = %q", hit.Name)
	}
	if hit.Brand != "Chick-fil-A" {
		t.Errorf("Brand = %q", hit.Brand)
	}
	if hit.ServingDescription != "4 minis" {
		t.Errorf("ServingDescription = %q, want %q", hit.ServingDescription, "4 minis")
	}
	want := Macros{Calories: 360, ProteinG: 19, FatG: 13, CarbsG: 41}
	if hit.PerServing != want {
		t.Errorf("PerServing = %+v, want %+v", hit.PerServing, want)
	}
	if hit.Source != "fatsecret" || hit.SourceID != "12345" {
		t.Errorf("Source/SourceID = %q/%q, want fatsecret/12345", hit.Source, hit.SourceID)
	}
}

func TestFatSecretTokenCachedAcrossSearches(t *testing.T) {
	var tokenCalls int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fsTokenJSON)
	}))
	t.Cleanup(tokenSrv.Close)
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"foods": {"food": [%s]}}`,
			fsFoodJSON("12345", "Chick-n-Minis (4 Count)", "Chick-fil-A", fsMinisDescription))
	}))
	t.Cleanup(searchSrv.Close)

	p := NewFatSecretProvider(searchSrv.Client(), "id", "secret", testLogger())
	p.TokenURL = tokenSrv.URL
	p.APIURL = searchSrv.URL

	if _, err := p.Search(context.Background(), "big mac", 5); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if _, err := p.Search(context.Background(), "whopper", 5); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if tokenCalls != 1 {
		t.Errorf("token endpoint called %d times, want 1 (token should be cached)", tokenCalls)
	}
}

func TestFatSecretSingleObjectFoodHandled(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fsTokenJSON)
	}))
	t.Cleanup(tokenSrv.Close)
	// Single hit: FatSecret's XML-converted JSON returns a bare object,
	// not a one-element array.
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"foods": {"food": %s}}`,
			fsFoodJSON("12345", "Chick-n-Minis (4 Count)", "Chick-fil-A", fsMinisDescription))
	}))
	t.Cleanup(searchSrv.Close)

	p := NewFatSecretProvider(searchSrv.Client(), "id", "secret", testLogger())
	p.TokenURL = tokenSrv.URL
	p.APIURL = searchSrv.URL

	hits, err := p.Search(context.Background(), "chicken minis", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(hits))
	}
	if hits[0].SourceID != "12345" {
		t.Errorf("SourceID = %q, want 12345", hits[0].SourceID)
	}
}

func TestFatSecretUnparseableDescriptionSkipped(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fsTokenJSON)
	}))
	t.Cleanup(tokenSrv.Close)
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"foods": {"food": [%s, %s]}}`,
			fsFoodJSON("1", "Mystery Food", "", "no macros here"),
			fsFoodJSON("12345", "Chick-n-Minis (4 Count)", "Chick-fil-A", fsMinisDescription))
	}))
	t.Cleanup(searchSrv.Close)

	p := NewFatSecretProvider(searchSrv.Client(), "id", "secret", testLogger())
	p.TokenURL = tokenSrv.URL
	p.APIURL = searchSrv.URL

	hits, err := p.Search(context.Background(), "chicken minis", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1 (unparseable description skipped, not guessed)", len(hits))
	}
	if hits[0].SourceID != "12345" {
		t.Errorf("SourceID = %q, want 12345", hits[0].SourceID)
	}
}

func TestFatSecretConfigured(t *testing.T) {
	tests := []struct {
		name         string
		clientID     string
		clientSecret string
		want         bool
	}{
		{"both empty", "", "", false},
		{"id only", "id", "", false},
		{"secret only", "", "secret", false},
		{"both set", "id", "secret", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewFatSecretProvider(http.DefaultClient, tt.clientID, tt.clientSecret, testLogger())
			if got := p.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

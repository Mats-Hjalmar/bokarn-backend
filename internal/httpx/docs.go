package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/joakimcarlsson/minmux/openapi"
	"github.com/joakimcarlsson/minmux/router"
	"github.com/joakimcarlsson/minmux/scalar"
)

func registerSpec(r *router.Router, gen *openapi.Generator) {
	r.HandleFunc(http.MethodGet, "/openapi.json", func(
		w http.ResponseWriter, _ *http.Request,
	) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(gen.Spec(r)); err != nil {
			logger.Error("encode openapi spec failed", "err", err)
		}
	})
}

func registerDocs(r *router.Router) {
	r.HandleFunc(http.MethodGet, "/docs", scalar.HandlerWith(scalar.Config{
		SpecURL: "/openapi.json",
		Title:   "bokarn API — Reference",
	}))
}

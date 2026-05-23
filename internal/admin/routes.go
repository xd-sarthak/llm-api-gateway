package admin

import (
	"github.com/go-chi/chi/v5"
)

func RegisterRoutes(r chi.Router) {
	r.Get("/metrics", Metrics)
	r.Get("/metrics/prometheus", PrometheusMetrics)
	r.Route("/admin", func(r chi.Router) {
		r.Get("/keys", ListKeys)
		r.Post("/keys", CreateKey)
		r.Delete("/keys/{id}", DeactivateKey)
		r.Get("/usage", GetUsage)
		r.Get("/stats", GetStats)
		r.Get("/metrics", Metrics)
		r.Get("/metrics/prometheus", PrometheusMetrics)
	})
}

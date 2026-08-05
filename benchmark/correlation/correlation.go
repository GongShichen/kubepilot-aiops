package correlation

import (
	"fmt"
	"math/rand/v2"
	"time"
)

type Alert struct {
	ID            string    `json:"id"`
	Fingerprint   string    `json:"fingerprint"`
	Namespace     string    `json:"namespace"`
	Service       string    `json:"service"`
	TraceID       string    `json:"trace_id,omitempty"`
	Revision      string    `json:"revision,omitempty"`
	StartsAt      time.Time `json:"starts_at"`
	ExpectedGroup string    `json:"expected_group"`
}

func Generate(groups, minAlerts, maxAlerts int, seed uint64) []Alert {
	rng := rand.New(rand.NewPCG(seed, seed^0x517cc1b727220a95))
	services := []string{"gateway-service", "order-service", "payment-service"}
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	var out []Alert
	for g := 0; g < groups; g++ {
		count := minAlerts + rng.IntN(maxAlerts-minAlerts+1)
		group := fmt.Sprintf("group-%03d", g+1)
		service := services[g%len(services)]
		traceID := fmt.Sprintf("trace-%03d", g+1)
		revision := fmt.Sprintf("rev-%03d", g+1)
		start := base.Add(time.Duration(g) * 10 * time.Minute)
		for i := 0; i < count; i++ {
			out = append(out, Alert{ID: fmt.Sprintf("alert-%03d-%02d", g+1, i+1), Fingerprint: fmt.Sprintf("fp-%03d-%02d", g+1, i%max(1, count-1)), Namespace: "kubepilot-benchmark", Service: service, TraceID: traceID, Revision: revision, StartsAt: start.Add(time.Duration(i) * 15 * time.Second), ExpectedGroup: group})
		}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
func Expected(items []Alert) map[string]string {
	out := map[string]string{}
	for _, a := range items {
		out[a.ID] = a.ExpectedGroup
	}
	return out
}

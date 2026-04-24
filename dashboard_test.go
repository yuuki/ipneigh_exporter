package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type grafanaDashboard struct {
	Title  string `json:"title"`
	Panels []struct {
		Title      string         `json:"title"`
		Datasource map[string]any `json:"datasource"`
		Targets    []struct {
			Expr       string         `json:"expr"`
			Datasource map[string]any `json:"datasource"`
		} `json:"targets"`
	} `json:"panels"`
	Templating struct {
		List []struct {
			Name       string         `json:"name"`
			Type       string         `json:"type"`
			AllValue   string         `json:"allValue"`
			Datasource map[string]any `json:"datasource"`
		} `json:"list"`
	} `json:"templating"`
}

func TestGrafanaDashboardTemplate(t *testing.T) {
	body, err := os.ReadFile("dashboards/ipneigh-exporter.json")
	if err != nil {
		t.Fatalf("read dashboard template: %v", err)
	}

	var dashboard grafanaDashboard
	if err := json.Unmarshal(body, &dashboard); err != nil {
		t.Fatalf("dashboard template is not valid JSON: %v", err)
	}

	if got, want := dashboard.Title, "ipneigh_exporter"; got != want {
		t.Fatalf("dashboard title = %v, want %q", got, want)
	}

	var exprs []string
	panels := make(map[string]bool)
	for _, panel := range dashboard.Panels {
		panels[panel.Title] = true
		if panel.Datasource["uid"] != "${DS_PROMETHEUS}" {
			t.Errorf("panel %q datasource uid = %v, want ${DS_PROMETHEUS}", panel.Title, panel.Datasource["uid"])
		}
		for _, target := range panel.Targets {
			exprs = append(exprs, target.Expr)
			if target.Datasource["uid"] != "${DS_PROMETHEUS}" {
				t.Errorf("panel %q target datasource uid = %v, want ${DS_PROMETHEUS}", panel.Title, target.Datasource["uid"])
			}
		}
	}

	for _, panel := range []string{
		"Exporter Status",
		"Neighbor Entries",
		"MAC Flaps",
		"Exporter Errors",
		"MAC Flaps by Device",
		"Neighbor Entries by State",
		"Current Entries by State",
		"Entries by Address Family",
		"Top Flapping Neighbors",
		"Last Flap Time",
		"Exporter Events",
		"Exporter Errors by Stage",
		"Rate-Limited Flaps",
	} {
		if !panels[panel] {
			t.Errorf("dashboard does not include %q panel", panel)
		}
	}

	allExprs := strings.Join(exprs, "\n")
	for _, metric := range []string{
		"linux_neighbor_mac_change_total",
		"linux_neighbor_entries",
		"linux_neighbor_last_flap_unix_seconds",
		"linux_neighbor_exporter_events_total",
		"linux_neighbor_exporter_errors_total",
	} {
		if !strings.Contains(allExprs, metric) {
			t.Errorf("dashboard does not reference %s", metric)
		}
	}

	variables := make(map[string]struct {
		Type     string
		AllValue string
	})
	for _, variable := range dashboard.Templating.List {
		variables[variable.Name] = struct {
			Type     string
			AllValue string
		}{Type: variable.Type, AllValue: variable.AllValue}
		if variable.Name != "DS_PROMETHEUS" && variable.Datasource["uid"] != "${DS_PROMETHEUS}" {
			t.Errorf("variable %q datasource uid = %v, want ${DS_PROMETHEUS}", variable.Name, variable.Datasource["uid"])
		}
	}

	for _, variable := range []string{"DS_PROMETHEUS", "job", "instance", "dev", "vrf", "family"} {
		if _, ok := variables[variable]; !ok {
			t.Errorf("dashboard does not define %s variable", variable)
		}
	}

	if variables["DS_PROMETHEUS"].Type != "datasource" {
		t.Errorf("DS_PROMETHEUS type = %q, want datasource", variables["DS_PROMETHEUS"].Type)
	}
	for _, variable := range []string{"job", "instance", "dev", "vrf", "family"} {
		if variables[variable].Type != "query" {
			t.Errorf("%s type = %q, want query", variable, variables[variable].Type)
		}
	}

	if variables["job"].AllValue == ".*" || variables["instance"].AllValue == ".*" {
		t.Errorf("job and instance All values must not expand to every Prometheus target")
	}

	exporterStatusExpr := panelExpr(t, dashboard, "Exporter Status")
	if !strings.Contains(exporterStatusExpr, "linux_neighbor_") {
		t.Errorf("Exporter Status query should be scoped to ipneigh_exporter metrics, got %q", exporterStatusExpr)
	}

	if !strings.Contains(allExprs, "${job:regex}") || !strings.Contains(allExprs, "${instance:regex}") {
		t.Errorf("dashboard queries should filter by job and instance variables")
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(readme), "dashboards/ipneigh-exporter.json") {
		t.Fatalf("README does not link to the Grafana dashboard template")
	}
}

func panelExpr(t *testing.T, dashboard grafanaDashboard, title string) string {
	t.Helper()
	for _, panel := range dashboard.Panels {
		if panel.Title == title {
			if len(panel.Targets) == 0 {
				t.Fatalf("panel %q has no targets", title)
			}
			return panel.Targets[0].Expr
		}
	}
	t.Fatalf("panel %q not found", title)
	return ""
}

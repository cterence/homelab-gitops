package main

import (
	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/stat"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// statGrid lays out small stat panels: 6 columns at short height so the
// number tiles stay compact instead of full-size panels.
func statGrid(panelNames ...string) *dashboardv2.AutoGridBuilder {
	grid := dashboardv2.NewAutoGridBuilder().
		MaxColumnCount(6).
		ColumnWidthMode(dashboardv2.AutoGridLayoutSpecColumnWidthModeStandard).
		RowHeightMode(dashboardv2.AutoGridLayoutSpecRowHeightModeShort)
	for _, name := range panelNames {
		grid = grid.WithItem(name)
	}

	return grid
}

// versionPanel shows the version label as the name and the agent count as
// the value; default stat text mode hides the name, so the version would
// not display.
func versionPanel() *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(1).
		Title("Cilium version").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", "count by (version) (cilium_version)", "{{version}}", true))).
		Visualization(stat.NewVisualizationV2Builder().
			Unit("none").
			ColorScheme(dashboardv2.NewFieldColorBuilder().
				Mode(dashboardv2.FieldColorModeIdThresholds).
				FixedColor("#ad46ff")).
			Thresholds(thresholds(thresholdStep("green", 0))).
			ColorMode(common.BigValueColorModeValue).
			TextMode(common.BigValueTextModeValueAndName).
			GraphMode(common.BigValueGraphModeNone).
			Orientation(common.VizOrientationAuto).
			ReduceOptions(lastNotNull()).
			WideLayout(true))
}

func ratePanel(id float64, title string, targets ...*dashboardv2.TargetBuilder) *dashboardv2.PanelBuilder {
	group := dashboardv2.NewQueryGroupBuilder()
	for _, t := range targets {
		group = group.Target(t)
	}

	return dashboardv2.NewPanelBuilder().
		Id(id).
		Title(title).
		Data(group).
		Visualization(timeseries.NewVisualizationV2Builder().
			Unit("ops").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
			Thresholds(thresholds(thresholdStep("green", 0))).
			DrawStyle(common.GraphDrawStyleLine).
			FillOpacity(25).
			GradientMode(common.GraphGradientModeNone).
			LineInterpolation(common.LineInterpolationLinear).
			LineWidth(1).
			ShowPoints(common.VisibilityModeAuto).
			Legend(common.NewVizLegendOptionsBuilder().
				DisplayMode(common.LegendDisplayModeList).
				Placement(common.LegendPlacementBottom).
				ShowLegend(true)).
			Tooltip(common.NewVizTooltipOptionsBuilder().
				Mode(common.TooltipDisplayModeSingle).
				Sort(common.SortOrderNone)))
}

func gaugePanel(id float64, title, expr, legend, unit string, max float64, steps []dashboardv2.Threshold) *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(id).
		Title(title).
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", expr, legend, true))).
		Visualization(bargauge.NewVisualizationV2Builder().
			Unit(unit).
			Max(max).
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdThresholds)).
			Thresholds(thresholds(steps...)).
			DisplayMode(common.BarGaugeDisplayModeBasic).
			Orientation(common.VizOrientationHorizontal).
			ValueMode(common.BarGaugeValueModeColor).
			NamePlacement(common.BarGaugeNamePlacementAuto).
			ShowUnfilled(true).
			Sizing(common.BarGaugeSizingAuto).
			MinVizWidth(8).
			MinVizHeight(16).
			ReduceOptions(lastNotNull()))
}

func okThreshold() []dashboardv2.Threshold {
	return []dashboardv2.Threshold{thresholdStep("green", 0), thresholdStep("dark-red", 0.001)}
}

func ciliumHealthPanels() []*dashboardv2.PanelBuilder {
	return []*dashboardv2.PanelBuilder{
		versionPanel(),
		statPanel(2, "Endpoints managed", `sum by (node) (cilium_endpoint)`, "{{node}}", "none", common.BigValueGraphModeNone, []dashboardv2.Threshold{thresholdStep("green", 0)}),
		statPanel(3, "Unreachable nodes", "max(cilium_unreachable_nodes)", "nodes", "none", common.BigValueGraphModeNone, okThreshold()),
		statPanel(4, "Failing controllers", "max(cilium_controllers_failing)", "controllers", "none", common.BigValueGraphModeNone, okThreshold()),
		statPanel(5, "Hubble lost events", `sum(rate(hubble_lost_events_total[5m]))`, "events/s", "ops", common.BigValueGraphModeArea, okThreshold()),
		statPanel(6, "Endpoint regen failures", `sum(rate(cilium_endpoint_regenerations_total{outcome="failed"}[5m]))`, "failures/s", "ops", common.BigValueGraphModeArea, okThreshold()),
	}
}

func cilium() *dashboardv2.DashboardBuilder {
	health := ciliumHealthPanels()

	return dashboardv2.NewDashboardBuilder("Cilium & Hubble").
		CursorSync(dashboardv2.DashboardCursorSyncOff).
		Editable(true).
		Annotations([]cog.Builder[dashboardv2.AnnotationQueryKind]{builtInAnnotation()}).
		Panel("panel-1", health[0]).
		Panel("panel-2", health[1]).
		Panel("panel-3", health[2]).
		Panel("panel-4", health[3]).
		Panel("panel-5", health[4]).
		Panel("panel-6", health[5]).
		Panel("panel-7", ratePanel(7, "Drop rate by reason",
			promTarget("A", `sum by (reason) (rate(hubble_drop_total{reason!="UNSUPPORTED_L3_PROTOCOL"}[5m]))`, "{{reason}}", false))).
		Panel("panel-8", ratePanel(8, "Policy-denied drops by node",
			promTarget("A", `sum by (node) (rate(hubble_drop_total{reason="POLICY_DENIED"}[5m]))`, "{{node}}", false))).
		Panel("panel-9", gaugePanel(9, "Drops last 24h by reason and protocol",
			`topk(10, sum by (reason, protocol) (increase(hubble_drop_total{reason!="UNSUPPORTED_L3_PROTOCOL"}[24h])))`, "{{reason}} {{protocol}}", "none", 0,
			[]dashboardv2.Threshold{thresholdStep("green", 0)})).
		Panel("panel-10", ratePanel(10, "Flows by verdict",
			promTarget("A", "sum by (verdict) (rate(hubble_flows_processed_total[5m]))", "{{verdict}}", false))).
		Panel("panel-11", ratePanel(11, "Flows by type",
			promTarget("A", "sum by (type, subtype) (rate(hubble_flows_processed_total[5m]))", "{{type}} {{subtype}}", false))).
		Panel("panel-12", ratePanel(12, "Forwarded vs dropped packets",
			promTarget("A", "sum(rate(cilium_forward_count_total[5m]))", "forwarded", false),
			promTarget("B", "sum(rate(cilium_drop_count_total[5m]))", "dropped", false))).
		Panel("panel-13", gaugePanel(13, "Pod CIDR pool usage",
			`sum(cilium_ip_addresses{family="ipv4"}) / sum(cilium_ipam_capacity)`, "pool", "percentunit", 1,
			[]dashboardv2.Threshold{thresholdStep("green", 0), thresholdStep("#EAB839", 0.8), thresholdStep("dark-red", 0.9)})).
		RowsLayout(dashboardv2.NewRowsBuilder().
			Row(dashboardv2.NewRowBuilder().
				Title("Health").
				Collapse(false).
				AutoGridLayout(statGrid("panel-1", "panel-2", "panel-3", "panel-4", "panel-5", "panel-6"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Drops").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-7", "panel-8", "panel-9"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Traffic").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-10", "panel-11", "panel-12"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Capacity").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-13")))).
		TimeSettings(dashboardv2.NewTimeSettingsBuilder().
			From("now-6h").
			To("now").
			AutoRefresh("30s").
			AutoRefreshIntervals([]string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"}).
			Timezone("browser").
			FiscalYearStartMonth(0).
			HideTimepicker(false))
}

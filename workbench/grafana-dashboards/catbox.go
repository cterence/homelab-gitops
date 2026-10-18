package main

// Catbox dashboard: the stash's spool occupancy against its cap and
// the mesh size, from the gauges `catbox serve` exposes at /metrics.
// Queries take max() because hostNetwork rollout targets overlap and
// would otherwise double every panel.

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

// spoolCapBytes is the stash's --max default (100G): the spool chart's
// red dashed line.
const spoolCapBytes = 107374182400

func linePanel(id float64, title, unit, expr, legend string, steps []dashboardv2.Threshold, thresholdStyle *common.GraphThresholdsStyleConfigBuilder) *dashboardv2.PanelBuilder {
	viz := timeseries.NewVisualizationV2Builder().
		Unit(unit).
		ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
		Thresholds(thresholds(steps...)).
		DrawStyle(common.GraphDrawStyleLine).
		FillOpacity(10).
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
			Sort(common.SortOrderNone))

	if thresholdStyle != nil {
		viz = viz.ThresholdsStyle(thresholdStyle)
	}

	return dashboardv2.NewPanelBuilder().
		Id(id).
		Title(title).
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", expr, legend, false))).
		Visualization(viz)
}

func catbox() *dashboardv2.DashboardBuilder {
	return dashboardv2.NewDashboardBuilder("Catbox").
		Description("catbox stash: spool occupancy and mesh size").
		CursorSync(dashboardv2.DashboardCursorSyncOff).
		Editable(true).
		Annotations([]cog.Builder[dashboardv2.AnnotationQueryKind]{builtInAnnotation()}).
		Panel("panel-1", statPanel(1, "Spool fill",
			"max(catbox_spool_bytes / catbox_spool_cap_bytes)", "fill", "percentunit", common.BigValueGraphModeNone, []dashboardv2.Threshold{
				thresholdStep("green", 0),
				thresholdStep("#EAB839", 0.8),
				thresholdStep("dark-red", 0.9),
			}, 2)).
		Panel("panel-2", statPanel(2, "Parked deliveries", "max(catbox_spool_items)",
			"parked", "short", common.BigValueGraphModeNone, []dashboardv2.Threshold{thresholdStep("green", 0)})).
		Panel("panel-3", statPanel(3, "Mesh members", "max(catbox_members)",
			"members", "short", common.BigValueGraphModeNone, []dashboardv2.Threshold{thresholdStep("green", 0)})).
		Panel("panel-4", linePanel(4, "Spool bytes", "bytes", "max(catbox_spool_bytes)", "spool bytes",
			[]dashboardv2.Threshold{thresholdStep("green", 0), thresholdStep("dark-red", spoolCapBytes)},
			common.NewGraphThresholdsStyleConfigBuilder().Mode(common.GraphThresholdsStyleModeDashed))).
		Panel("panel-5", linePanel(5, "Parked deliveries over time", "short", "max(catbox_spool_items)", "parked",
			[]dashboardv2.Threshold{thresholdStep("green", 0)}, nil)).
		RowsLayout(dashboardv2.NewRowsBuilder().
			Row(dashboardv2.NewRowBuilder().
				Title("Spool").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-1", "panel-2", "panel-3"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Over time").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-4", "panel-5")))).
		TimeSettings(dashboardv2.NewTimeSettingsBuilder().
			From("now-6h").
			To("now").
			AutoRefresh("30s").
			AutoRefreshIntervals([]string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"}).
			Timezone("browser").
			FiscalYearStartMonth(0).
			HideTimepicker(false))
}

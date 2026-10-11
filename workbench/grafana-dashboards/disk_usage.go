package main

import (
	"fmt"

	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/stat"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

const promDatasource = "prometheus"

// grafanaQuery is the built-in "-- Grafana --" annotation query, which has no
// typed builder in the SDK.
type grafanaQuery struct{}

func (grafanaQuery) Build() (dashboardv2.DataQueryKind, error) {
	return dashboardv2.DataQueryKind{
		Kind:    "DataQuery",
		Group:   "grafana",
		Version: "v0",
		Datasource: &dashboardv2.Dashboardv2DataQueryKindDatasource{
			Name: cog.ToPtr("-- Grafana --"),
		},
		Spec: map[string]any{},
	}, nil
}

func builtInAnnotation() *dashboardv2.AnnotationQueryBuilder {
	return dashboardv2.NewAnnotationQueryBuilder().
		BuiltIn(true).
		Enable(true).
		Hide(true).
		IconColor("rgba(0, 211, 255, 1)").
		Name("Annotations & Alerts").
		Query(grafanaQuery{})
}

func promTarget(refID, expr, legend string, instant bool) *dashboardv2.TargetBuilder {
	return dashboardv2.NewTargetBuilder().
		RefId(refID).
		Query(prometheus.NewQueryV2Builder().
			Datasource(dashboardv2.NewDashboardv2DataQueryKindDatasourceBuilder().Name(promDatasource)).
			EditorMode(prometheus.QueryEditorModeCode).
			Exemplar(false).
			Expr(expr).
			Instant(instant).
			LegendFormat(legend).
			Range(!instant))
}

func renameByRegex(regex string) *dashboardv2.TransformationBuilder {
	return dashboardv2.NewTransformationBuilder().
		Group("renameByRegex").
		Options(map[string]any{"regex": regex, "renamePattern": "$1"})
}

func thresholds(steps ...dashboardv2.Threshold) *dashboardv2.ThresholdsConfigBuilder {
	return dashboardv2.NewThresholdsConfigBuilder().
		Mode(dashboardv2.ThresholdsModeAbsolute).
		Steps(steps)
}

func thresholdStep(color string, value float64) dashboardv2.Threshold {
	return dashboardv2.Threshold{Value: cog.ToPtr(value), Color: color}
}

func lastNotNull() *common.ReduceDataOptionsBuilder {
	return common.NewReduceDataOptionsBuilder().Calcs([]string{"lastNotNull"})
}

func volumeUsagePanel() *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(1).
		Title("Volume usage").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", "node_disk_usage_bytes", "{{path}}", false)).
			Transformation(renameByRegex(`\/data\/pvc-[\d\w\-]+_(.+)`))).
		Visualization(timeseries.NewVisualizationV2Builder().
			Unit("bytes").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
			Thresholds(thresholds(thresholdStep("green", 0))).
			DrawStyle(common.GraphDrawStyleLine).
			FillOpacity(25).
			GradientMode(common.GraphGradientModeNone).
			LineInterpolation(common.LineInterpolationLinear).
			LineWidth(1).
			ShowPoints(common.VisibilityModeAuto).
			Stacking(common.NewStackingConfigBuilder().Mode(common.StackingModeNormal).Group("A")).
			ThresholdsStyle(common.NewGraphThresholdsStyleConfigBuilder().Mode(common.GraphThresholdsStyleModeDashed)).
			Legend(common.NewVizLegendOptionsBuilder().
				DisplayMode(common.LegendDisplayModeList).
				Placement(common.LegendPlacementBottom).
				ShowLegend(true)).
			Tooltip(common.NewVizTooltipOptionsBuilder().
				Mode(common.TooltipDisplayModeSingle).
				Sort(common.SortOrderNone)))
}

func topUsagePanel() *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(4).
		Title("Top 10 by absolute usage").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", "topk(10, node_disk_usage_bytes)", "{{path}}", true)).
			Transformation(renameByRegex(`.+\/pvc-[\d\w\-]+_(.+)`))).
		Visualization(bargauge.NewVisualizationV2Builder().
			Unit("bytes").
			Max(1967847137280).
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdThresholds)).
			Thresholds(thresholds(thresholdStep("green", 0))).
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

func statPanel(id float64, title, expr, unit string, graphMode common.BigValueGraphMode, steps []dashboardv2.Threshold) *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(id).
		Title(title).
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", expr, "__auto", false))).
		Visualization(stat.NewVisualizationV2Builder().
			Unit(unit).
			ColorScheme(dashboardv2.NewFieldColorBuilder().
				Mode(dashboardv2.FieldColorModeIdThresholds).
				FixedColor("#ad46ff")).
			Thresholds(thresholds(steps...)).
			ColorMode(common.BigValueColorModeValue).
			GraphMode(graphMode).
			ReduceOptions(lastNotNull()).
			WideLayout(true))
}

// diskStatsPanels returns the capacity/used/relative stat triplet for one PVC.
func diskStatsPanels(pvc string, capacityID, usedID, relativeID int) []*dashboardv2.PanelBuilder {
	return []*dashboardv2.PanelBuilder{
		statPanel(float64(capacityID), "Absolute capacity",
			fmt.Sprintf("avg(kubelet_volume_stats_capacity_bytes{persistentvolumeclaim=%q})", pvc),
			"bytes", common.BigValueGraphModeNone, []dashboardv2.Threshold{thresholdStep("green", 0)}),
		statPanel(float64(usedID), "Absolute used space",
			fmt.Sprintf("avg(kubelet_volume_stats_used_bytes{persistentvolumeclaim=%q})", pvc),
			"bytes", common.BigValueGraphModeArea, []dashboardv2.Threshold{thresholdStep("green", 0)}),
		statPanel(float64(relativeID), "Relative used space",
			fmt.Sprintf("1 - kubelet_volume_stats_available_bytes{persistentvolumeclaim=%q}/kubelet_volume_stats_capacity_bytes{persistentvolumeclaim=%q}", pvc, pvc),
			"percentunit", common.BigValueGraphModeNone, []dashboardv2.Threshold{
				thresholdStep("green", 0),
				thresholdStep("#EAB839", 0.8),
				thresholdStep("dark-red", 0.9),
			}),
	}
}

func autoGrid(panelNames ...string) *dashboardv2.AutoGridBuilder {
	grid := dashboardv2.NewAutoGridBuilder().
		MaxColumnCount(3).
		ColumnWidthMode(dashboardv2.AutoGridLayoutSpecColumnWidthModeStandard).
		RowHeightMode(dashboardv2.AutoGridLayoutSpecRowHeightModeStandard)
	for _, name := range panelNames {
		grid = grid.WithItem(name)
	}

	return grid
}

func diskUsage() *dashboardv2.DashboardBuilder {
	homelab2 := diskStatsPanels("arr-stack-data", 5, 2, 3)
	homelab3 := diskStatsPanels("storage-loki-0", 9, 10, 11)
	pool := diskStatsPanels("storage-pool", 6, 7, 8)

	return dashboardv2.NewDashboardBuilder("Disk usage").
		CursorSync(dashboardv2.DashboardCursorSyncOff).
		Editable(true).
		Annotations([]cog.Builder[dashboardv2.AnnotationQueryKind]{builtInAnnotation()}).
		Panel("panel-1", volumeUsagePanel()).
		Panel("panel-4", topUsagePanel()).
		Panel("panel-5", homelab2[0]).
		Panel("panel-2", homelab2[1]).
		Panel("panel-3", homelab2[2]).
		Panel("panel-9", homelab3[0]).
		Panel("panel-10", homelab3[1]).
		Panel("panel-11", homelab3[2]).
		Panel("panel-6", pool[0]).
		Panel("panel-7", pool[1]).
		Panel("panel-8", pool[2]).
		RowsLayout(dashboardv2.NewRowsBuilder().
			Row(dashboardv2.NewRowBuilder().
				Title("Per volume stats").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-1", "panel-4"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Disk stats (SSD homelab2)").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-5", "panel-2", "panel-3"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Disk stats (SSD homelab3)").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-9", "panel-10", "panel-11"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Disk stats (pool)").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-6", "panel-7", "panel-8")))).
		TimeSettings(dashboardv2.NewTimeSettingsBuilder().
			From("now-6h").
			To("now").
			AutoRefresh("30s").
			AutoRefreshIntervals([]string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"}).
			Timezone("browser").
			FiscalYearStartMonth(0).
			HideTimepicker(false)).
		ConstantVariable(dashboardv2.NewConstantVariableBuilder("max_capacity").
			Query("1967847152828").
			Current(dashboardv2.VariableOption{
				Text:  dashboardv2.StringOrArrayOfString{String: cog.ToPtr("1967847152828")},
				Value: dashboardv2.StringOrArrayOfString{String: cog.ToPtr("1967847152828")},
			}).
			Hide(dashboardv2.VariableHideHideVariable).
			SkipUrlSync(true))
}

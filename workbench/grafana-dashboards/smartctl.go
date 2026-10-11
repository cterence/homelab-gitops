package main

import (
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

const (
	reallocatedSectors = `smartctl_device_attribute{attribute_name="Reallocated_Sector_Ct",attribute_value_type="raw"}`
	pendingSectors     = `smartctl_device_attribute{attribute_name="Current_Pending_Sector",attribute_value_type="raw"}`
	powerOnHours       = `smartctl_device_attribute{attribute_name="Power_On_Hours",attribute_value_type="raw"}`
)

func smartStatPanel(id int, title, expr, unit string, steps []dashboardv2.Threshold) *dashboardv2.PanelBuilder {
	return statPanel(float64(id), title, expr, unit, common.BigValueGraphModeNone, steps)
}

func temperatureTimeseriesPanel() *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(6).
		Title("Temperature").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", "smartctl_device_temperature", "{{device}}", false))).
		Visualization(timeseries.NewVisualizationV2Builder().
			Unit("celsius").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
			Thresholds(thresholds(thresholdStep("green", 0), thresholdStep("#EAB839", 45), thresholdStep("dark-red", 60))).
			DrawStyle(common.GraphDrawStyleLine).
			FillOpacity(0).
			GradientMode(common.GraphGradientModeNone).
			LineInterpolation(common.LineInterpolationLinear).
			LineWidth(1).
			ShowPoints(common.VisibilityModeAuto).
			ThresholdsStyle(common.NewGraphThresholdsStyleConfigBuilder().Mode(common.GraphThresholdsStyleModeDashed)).
			Legend(common.NewVizLegendOptionsBuilder().
				DisplayMode(common.LegendDisplayModeList).
				Placement(common.LegendPlacementBottom).
				ShowLegend(true)).
			Tooltip(common.NewVizTooltipOptionsBuilder().
				Mode(common.TooltipDisplayModeSingle).
				Sort(common.SortOrderNone)))
}

func sectorErrorsPanel() *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(7).
		Title("Sector errors").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", reallocatedSectors, "{{device}} {{attribute_name}}", false)).
			Target(promTarget("B", pendingSectors, "{{device}} {{attribute_name}}", false))).
		Visualization(timeseries.NewVisualizationV2Builder().
			Unit("short").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
			Thresholds(thresholds(thresholdStep("green", 0), thresholdStep("dark-red", 1))).
			DrawStyle(common.GraphDrawStyleLine).
			FillOpacity(0).
			GradientMode(common.GraphGradientModeNone).
			LineInterpolation(common.LineInterpolationLinear).
			LineWidth(1).
			ShowPoints(common.VisibilityModeAuto).
			ThresholdsStyle(common.NewGraphThresholdsStyleConfigBuilder().Mode(common.GraphThresholdsStyleModeDashed)).
			Legend(common.NewVizLegendOptionsBuilder().
				DisplayMode(common.LegendDisplayModeList).
				Placement(common.LegendPlacementBottom).
				ShowLegend(true)).
			Tooltip(common.NewVizTooltipOptionsBuilder().
				Mode(common.TooltipDisplayModeSingle).
				Sort(common.SortOrderNone)))
}

func smartctl() *dashboardv2.DashboardBuilder {
	return dashboardv2.NewDashboardBuilder("SMART health").
		CursorSync(dashboardv2.DashboardCursorSyncOff).
		Editable(true).
		Annotations([]cog.Builder[dashboardv2.AnnotationQueryKind]{builtInAnnotation()}).
		Panel("panel-1", smartStatPanel(1, "SMART status", "smartctl_device_smart_status", "none", []dashboardv2.Threshold{
			thresholdStep("dark-red", 0),
			thresholdStep("green", 1),
		})).
		Panel("panel-2", smartStatPanel(2, "Temperature", "smartctl_device_temperature", "celsius", []dashboardv2.Threshold{
			thresholdStep("green", 0),
			thresholdStep("#EAB839", 45),
			thresholdStep("dark-red", 60),
		})).
		Panel("panel-3", smartStatPanel(3, "Reallocated sectors", reallocatedSectors, "short", []dashboardv2.Threshold{
			thresholdStep("green", 0),
			thresholdStep("dark-red", 1),
		})).
		Panel("panel-4", smartStatPanel(4, "Pending sectors", pendingSectors, "short", []dashboardv2.Threshold{
			thresholdStep("green", 0),
			thresholdStep("dark-red", 1),
		})).
		Panel("panel-5", smartStatPanel(5, "Power on hours", powerOnHours, "short", []dashboardv2.Threshold{
			thresholdStep("green", 0),
		})).
		Panel("panel-6", temperatureTimeseriesPanel()).
		Panel("panel-7", sectorErrorsPanel()).
		RowsLayout(dashboardv2.NewRowsBuilder().
			Row(dashboardv2.NewRowBuilder().
				Title("Device health").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-1", "panel-2", "panel-3", "panel-4", "panel-5"))).
			Row(dashboardv2.NewRowBuilder().
				Title("History").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-6", "panel-7")))).
		TimeSettings(dashboardv2.NewTimeSettingsBuilder().
			From("now-6h").
			To("now").
			AutoRefresh("30s").
			AutoRefreshIntervals([]string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"}).
			Timezone("browser").
			FiscalYearStartMonth(0).
			HideTimepicker(false))
}

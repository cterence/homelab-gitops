package main

import (
	"fmt"

	"github.com/grafana/grafana-foundation-sdk/go/bargauge"
	"github.com/grafana/grafana-foundation-sdk/go/cog"
	"github.com/grafana/grafana-foundation-sdk/go/common"
	dashboardv2 "github.com/grafana/grafana-foundation-sdk/go/dashboardv2"
	"github.com/grafana/grafana-foundation-sdk/go/prometheus"
	"github.com/grafana/grafana-foundation-sdk/go/table"
	"github.com/grafana/grafana-foundation-sdk/go/timeseries"
)

const (
	trivyVulns   = `trivy_image_vulnerabilities`
	trivySecrets = `trivy_image_exposedsecrets`
	trivyAudits  = `trivy_resource_configaudits`
	nsMatch      = `namespace=~"$namespace"`
)

func trivyNamespaceVariable() dashboardv2.VariableKind {
	all := dashboardv2.VariableOption{
		Selected: cog.ToPtr(true),
		Text:     dashboardv2.StringOrArrayOfString{String: cog.ToPtr("All")},
		Value:    dashboardv2.StringOrArrayOfString{String: cog.ToPtr("$__all")},
	}

	kind := dashboardv2.QueryVariableKind{
		Kind: "QueryVariable",
		Spec: dashboardv2.QueryVariableSpec{
			Name:       "namespace",
			Label:      cog.ToPtr("Namespace"),
			Hide:       dashboardv2.VariableHideDontHide,
			Refresh:    dashboardv2.VariableRefreshOnDashboardLoad,
			Sort:       dashboardv2.VariableSortAlphabeticalAsc,
			IncludeAll: true,
			AllValue:   cog.ToPtr(".*"),
			Current:    all,
			Query: dashboardv2.DataQueryKind{
				Kind:    "DataQuery",
				Group:   "prometheus",
				Version: "v0",
				Datasource: &dashboardv2.Dashboardv2DataQueryKindDatasource{
					Name: cog.ToPtr(promDatasource),
				},
				Spec: map[string]any{
					"expr":  "label_values(trivy_image_vulnerabilities, namespace)",
					"refId": "A",
				},
			},
		},
	}

	return dashboardv2.VariableKind{QueryVariableKind: &kind}
}

func severityStatPanel(id float64, title, severity string, steps []dashboardv2.Threshold) *dashboardv2.PanelBuilder {
	expr := fmt.Sprintf(`sum(%s{%s,severity=%q})`, trivyVulns, nsMatch, severity)
	return statPanel(id, title, expr, severity, "short", common.BigValueGraphModeNone, steps)
}

func imagesWithCriticalsPanel() *dashboardv2.PanelBuilder {
	expr := fmt.Sprintf(`count(count by (image_digest) (%s{%s,severity=~"Critical|High"}))`, trivyVulns, nsMatch)

	return statPanel(3, "Images with critical or high", expr, "images", "short", common.BigValueGraphModeNone, []dashboardv2.Threshold{
		thresholdStep("green", 0),
		thresholdStep("#EAB839", 5),
		thresholdStep("dark-red", 15),
	})
}

func exposedSecretsPanel() *dashboardv2.PanelBuilder {
	return statPanel(4, "Exposed secrets", fmt.Sprintf("sum(%s{%s})", trivySecrets, nsMatch), "secrets", "short", common.BigValueGraphModeNone, []dashboardv2.Threshold{
		thresholdStep("green", 0),
		thresholdStep("dark-red", 1),
	})
}

func stackedSeverityPanel(id float64, title, metric string) *dashboardv2.PanelBuilder {
	return dashboardv2.NewPanelBuilder().
		Id(id).
		Title(title).
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", fmt.Sprintf("sum by (severity) (%s{%s})", metric, nsMatch), "{{severity}}", false))).
		Visualization(timeseries.NewVisualizationV2Builder().
			Unit("short").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdPaletteClassic)).
			Thresholds(thresholds(thresholdStep("green", 0))).
			DrawStyle(common.GraphDrawStyleBars).
			FillOpacity(40).
			GradientMode(common.GraphGradientModeOpacity).
			LineInterpolation(common.LineInterpolationStepAfter).
			LineWidth(0).
			ShowPoints(common.VisibilityModeAuto).
			Stacking(common.NewStackingConfigBuilder().Mode(common.StackingModeNormal).Group("A")).
			Legend(common.NewVizLegendOptionsBuilder().
				DisplayMode(common.LegendDisplayModeList).
				Placement(common.LegendPlacementBottom).
				ShowLegend(true)).
			Tooltip(common.NewVizTooltipOptionsBuilder().
				Mode(common.TooltipDisplayModeMulti).
				Sort(common.SortOrderDescending)))
}

func topImagesPanel() *dashboardv2.PanelBuilder {
	expr := fmt.Sprintf(`topk(10, sum by (namespace, image_repository, image_tag) (%s{%s,severity=~"Critical|High"}))`, trivyVulns, nsMatch)

	return dashboardv2.NewPanelBuilder().
		Id(7).
		Title("Top 10 images by critical or high").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", expr, "{{namespace}} {{image_repository}}:{{image_tag}}", true))).
		Visualization(bargauge.NewVisualizationV2Builder().
			Unit("short").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdThresholds)).
			Thresholds(thresholds(thresholdStep("green", 0), thresholdStep("#EAB839", 10), thresholdStep("dark-red", 50))).
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

// promTableTarget is promTarget for instant queries rendered as a single
// table frame (labels as columns), instead of one frame per series.
func promTableTarget(refID, expr string) *dashboardv2.TargetBuilder {
	return dashboardv2.NewTargetBuilder().
		RefId(refID).
		Query(prometheus.NewQueryV2Builder().
			Datasource(dashboardv2.NewDashboardv2DataQueryKindDatasourceBuilder().Name(promDatasource)).
			EditorMode(prometheus.QueryEditorModeCode).
			Exemplar(false).
			Expr(expr).
			Format(prometheus.PromQueryFormatTable).
			Instant(true).
			Range(false))
}

func topWorkloadsPanel() *dashboardv2.PanelBuilder {
	expr := fmt.Sprintf(`topk(15, sum by (namespace, resource_name, image_repository, image_tag) (%s{%s,severity="Critical"}))`, trivyVulns, nsMatch)

	return dashboardv2.NewPanelBuilder().
		Id(8).
		Title("Top 15 workloads by criticals").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTableTarget("A", expr)).
			Transformation(dashboardv2.NewTransformationBuilder().
				Group("organize").
				Options(map[string]any{
					"excludeByName": map[string]any{"Time": true},
					"indexByName": map[string]any{
						"namespace": 0, "resource_name": 1,
						"image_repository": 2, "image_tag": 3, "value": 4,
					},
					"renameByName": map[string]any{
						"resource_name": "workload",
						"value":         "critical",
						"Value":         "critical",
					},
				}))).
		Visualization(table.NewVisualizationV2Builder().
			Unit("short").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdThresholds)).
			Thresholds(thresholds(thresholdStep("green", 0), thresholdStep("dark-red", 1))).
			ShowHeader(true).
			SortBy([]cog.Builder[common.TableSortByFieldState]{
				common.NewTableSortByFieldStateBuilder().DisplayName("critical").Desc(true),
			}))
}

func criticalsByNamespacePanel() *dashboardv2.PanelBuilder {
	expr := fmt.Sprintf(`topk(15, sum by (namespace) (%s{%s,severity="Critical"}))`, trivyVulns, nsMatch)

	return dashboardv2.NewPanelBuilder().
		Id(9).
		Title("Criticals by namespace").
		Data(dashboardv2.NewQueryGroupBuilder().
			Target(promTarget("A", expr, "{{namespace}}", true))).
		Visualization(bargauge.NewVisualizationV2Builder().
			Unit("short").
			ColorScheme(dashboardv2.NewFieldColorBuilder().Mode(dashboardv2.FieldColorModeIdThresholds)).
			Thresholds(thresholds(thresholdStep("green", 0), thresholdStep("dark-red", 10))).
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

func trivy() *dashboardv2.DashboardBuilder {
	return dashboardv2.NewDashboardBuilder("Trivy security").
		CursorSync(dashboardv2.DashboardCursorSyncOff).
		Editable(true).
		Annotations([]cog.Builder[dashboardv2.AnnotationQueryKind]{builtInAnnotation()}).
		Variables([]dashboardv2.VariableKind{trivyNamespaceVariable()}).
		Panel("panel-1", severityStatPanel(1, "Critical", "Critical", []dashboardv2.Threshold{
			thresholdStep("green", 0), thresholdStep("#EAB839", 5), thresholdStep("dark-red", 20),
		})).
		Panel("panel-2", severityStatPanel(2, "High", "High", []dashboardv2.Threshold{
			thresholdStep("green", 0), thresholdStep("#EAB839", 20), thresholdStep("dark-red", 50),
		})).
		Panel("panel-3", imagesWithCriticalsPanel()).
		Panel("panel-4", exposedSecretsPanel()).
		Panel("panel-5", stackedSeverityPanel(5, "Vulnerabilities by severity", trivyVulns)).
		Panel("panel-6", stackedSeverityPanel(6, "Config audit findings by severity", trivyAudits)).
		Panel("panel-7", topImagesPanel()).
		Panel("panel-8", topWorkloadsPanel()).
		Panel("panel-9", criticalsByNamespacePanel()).
		RowsLayout(dashboardv2.NewRowsBuilder().
			Row(dashboardv2.NewRowBuilder().
				Title("Exposure").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-1", "panel-2", "panel-3", "panel-4"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Trends").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-5", "panel-6"))).
			Row(dashboardv2.NewRowBuilder().
				Title("Top offenders").
				Collapse(false).
				AutoGridLayout(autoGrid("panel-7", "panel-8", "panel-9")))).
		TimeSettings(dashboardv2.NewTimeSettingsBuilder().
			From("now-7d").
			To("now").
			AutoRefresh("5m").
			AutoRefreshIntervals([]string{"1m", "5m", "15m", "30m", "1h", "6h", "1d", "7d"}).
			Timezone("browser").
			FiscalYearStartMonth(0).
			HideTimepicker(false))
}

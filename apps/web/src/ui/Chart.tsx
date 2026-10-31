import { LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, MarkAreaComponent, MarkLineComponent, TooltipComponent } from 'echarts/components'
import * as echarts from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { useEffect, useRef } from 'react'

echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, MarkLineComponent, MarkAreaComponent, CanvasRenderer])

export type ChartOption = echarts.EChartsCoreOption

/** Reads the theme tokens so charts follow light/dark mode. */
export function themeColors() {
  const css = getComputedStyle(document.documentElement)
  const v = (name: string) => css.getPropertyValue(name).trim()
  return { fg: v('--fg'), muted: v('--muted'), line: v('--line'), panel: v('--panel'), bad: v('--bad'), warn: v('--warn'), ok: v('--ok'), accent: v('--accent') }
}

export const SERIES_COLORS = ['#3b7ae8', '#e0533d', '#0f9d8a', '#d98a0b', '#8b5cf6', '#0ea5b7', '#c2417f', '#7c8799']

/**
 * ECharts canvas. Tooltips render as rich text on the canvas instead of HTML,
 * so names from a (possibly shared, untrusted) design are never parsed as markup.
 */
export function Chart({ option, height, group }: { option: ChartOption; height: number; group?: string }) {
  const el = useRef<HTMLDivElement>(null)
  const chart = useRef<echarts.ECharts>()

  useEffect(() => {
    const c = echarts.init(el.current!, undefined, { renderer: 'canvas' })
    chart.current = c
    if (group) {
      c.group = group
      echarts.connect(group)
    }
    const ro = new ResizeObserver(() => c.resize())
    ro.observe(el.current!)
    return () => {
      ro.disconnect()
      c.dispose()
    }
  }, [group])

  useEffect(() => {
    const t = themeColors()
    chart.current?.setOption(
      {
        animation: false,
        textStyle: { color: t.muted, fontFamily: 'ui-sans-serif, system-ui, sans-serif', fontSize: 11 },
        color: SERIES_COLORS,
        tooltip: { trigger: 'axis', renderMode: 'richText', backgroundColor: t.panel, borderColor: t.line, textStyle: { color: t.fg, fontSize: 11 } },
        ...option,
      },
      { notMerge: true },
    )
  }, [option])

  return <div ref={el} style={{ height }} className="w-full" />
}

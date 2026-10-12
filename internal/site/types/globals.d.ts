// Globals from web/vendor/*.js, loaded on demand by modules/term.js.
declare const Terminal: any;
declare const FitAddon: { FitAddon: any };
declare const Unicode11Addon: { Unicode11Addon: any };

interface HTMLElement {
  /** options attached by modules/charts.js */
  __chart?: ChartOpts;
}

interface ChartSeries {
  label: string;
  color: string;
  data: (number | null)[];
  area?: boolean;
  dash?: boolean;
  width?: number;
  /** area top opacity (default 0.38) */
  fill?: number;
  /** plot against the right axis (ChartOpts.fmt2) */
  right?: boolean;
  /** tooltip row only: no stroke, no fill, no legend */
  tipOnly?: boolean;
  /** draw and show in the tooltip, but omit the bottom legend swatch */
  legend?: boolean;
}

interface ChartOpts {
  t: number[];
  series: ChartSeries[];
  fmt: (v: number | null, axis?: boolean) => string;
  /** right-axis formatter; enables the axis when a series has right */
  fmt2?: (v: number | null, axis?: boolean) => string;
  stepSec?: number;
  from?: number;
  to?: number;
  max?: number | null;
  stacked?: boolean;
  totalLabel?: string;
  /** extra tooltip rows after the series, for values that are not drawn */
  tipExtra?: (i: number) => string;
  capacity?: number | null;
  empty?: string;
}

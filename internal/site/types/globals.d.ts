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
}

interface ChartOpts {
  t: number[];
  series: ChartSeries[];
  fmt: (v: number | null, axis?: boolean) => string;
  stepSec?: number;
  from?: number;
  to?: number;
  max?: number | null;
  stacked?: boolean;
  totalLabel?: string;
  capacity?: number | null;
  empty?: string;
}

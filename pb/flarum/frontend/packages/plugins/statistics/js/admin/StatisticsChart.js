// The statistics line chart.
//
// Upstream draws it with `frappe-charts` (`import { Chart } from
// 'frappe-charts'`). This port has no such dependency — adding one for a
// single admin page is a dependency decision, and the chart is a polyline
// over twenty-eight points — so the drawing is inline SVG. What matters is
// that the SHAPE matches upstream's: two series (this period and the one
// before it), the same colours, the same axis modes.
//
// Everything here is presentation: the numbers arrive already bucketed from
// `buildSeries`, so a wrong-looking chart is a styling bug and never an
// arithmetic one.

import Component from 'flarum/common/Component';

/**
 * The plot box, in viewBox units. The viewBox is scaled by CSS, so these are
 * not pixels — they only set the aspect ratio and the padding.
 */
const WIDTH = 720;
const HEIGHT = 280;
const PADDING_LEFT = 44;
const PADDING_RIGHT = 12;
const PADDING_TOP = 12;
const PADDING_BOTTOM = 28;

export default class StatisticsChart extends Component {
  view() {
    const { labels, datasets, labelFor } = this.attrs;

    if (!labels || labels.length === 0) {
      return (
        <div className="StatisticsChart StatisticsChart--empty">
          <span>There is nothing to chart for this period.</span>
        </div>
      );
    }

    // The y scale is shared by BOTH series, so the comparison line is
    // drawn against the same axis — a per-series scale would make the two
    // lines incomparable, which is the whole point of drawing two.
    const peak = Math.max(1, ...datasets.flatMap((dataset) => dataset.values));
    const innerWidth = WIDTH - PADDING_LEFT - PADDING_RIGHT;
    const innerHeight = HEIGHT - PADDING_TOP - PADDING_BOTTOM;
    const stepX = labels.length > 1 ? innerWidth / (labels.length - 1) : 0;

    const point = (value, index) => {
      const x = PADDING_LEFT + stepX * index;
      const y = PADDING_TOP + innerHeight - (value / peak) * innerHeight;

      return [x, y];
    };

    // Four horizontal rules is enough to read a value off, and more would
    // be noise at this height.
    const gridLines = [0, 0.25, 0.5, 0.75, 1].map((fraction) => {
      const y = PADDING_TOP + innerHeight - fraction * innerHeight;

      return (
        <g>
          <line className="StatisticsChart-gridLine" x1={PADDING_LEFT} x2={WIDTH - PADDING_RIGHT} y1={y} y2={y} />
          <text className="StatisticsChart-axisLabel" x={PADDING_LEFT - 8} y={y + 4} text-anchor="end">
            {Math.round(peak * fraction)}
          </text>
        </g>
      );
    });

    // Label every Nth point, so a 365-point year does not become a smear.
    const labelEvery = Math.max(1, Math.ceil(labels.length / 12));

    return (
      <div className="StatisticsChart">
        <svg className="StatisticsChart-svg" viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label="Statistics chart">
          {gridLines}

          {datasets.map((dataset, seriesIndex) => {
            const points = dataset.values.map((value, index) => point(value, index).join(',')).join(' ');

            return (
              <polyline
                className={`StatisticsChart-line StatisticsChart-line--${seriesIndex === 0 ? 'current' : 'previous'}`}
                points={points}
                fill="none"
                stroke-width="2"
              />
            );
          })}

          {labels.map((label, index) =>
            index % labelEvery === 0 ? (
              <text
                className="StatisticsChart-axisLabel"
                x={PADDING_LEFT + stepX * index}
                y={HEIGHT - 8}
                text-anchor="middle"
              >
                {labelFor(label)}
              </text>
            ) : null
          )}
        </svg>

        <ul className="StatisticsChart-legend">
          {datasets.map((dataset, seriesIndex) => (
            <li className={`StatisticsChart-legendItem StatisticsChart-legendItem--${seriesIndex === 0 ? 'current' : 'previous'}`}>
              <span className="StatisticsChart-legendSwatch" />
              {dataset.name}
            </li>
          ))}
        </ul>
      </div>
    );
  }
}

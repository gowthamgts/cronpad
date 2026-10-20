import type { SVGProps } from "react";

/** The clock ring follows ink; the hand always carries the brand signal. */
export function Logo(props: SVGProps<SVGSVGElement>) {
  return <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" shapeRendering="crispEdges" aria-hidden="true" {...props}>
    <path fill="currentColor" d="M5 1h6v1h2v2h1v2h1v4h-1v2h-1v2h-2v1H5v-1H3v-2H2v-2H1V6h1V4h1V2h2V1Zm0 2v1H4v2H3v4h1v2h1v1h6v-1h1v-2h1V6h-1V4h-1V3H5Z" />
    <path fill="#c4f434" d="M7 4h2v4h3v2H7V4Z" />
  </svg>;
}

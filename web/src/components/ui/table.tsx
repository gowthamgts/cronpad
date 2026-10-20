import * as React from "react";
import { cn } from "@/lib/utils";

const Table = React.forwardRef<HTMLTableElement, React.HTMLAttributes<HTMLTableElement>>(({ className, ...props }, ref) => (
  // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- keyboard access to horizontally scrolling tables
  <div tabIndex={0} className="table-scroll relative min-w-0 rounded-md border border-line bg-surface">
    <table ref={ref} className={cn("w-full caption-bottom text-sm tabular-nums", className)} {...props} />
  </div>
));
Table.displayName = "Table";

const TableHeader = (props: React.HTMLAttributes<HTMLTableSectionElement>) => <thead className="bg-sunken/70 [&_tr]:border-b [&_tr]:border-line" {...props} />;
const TableBody = (props: React.HTMLAttributes<HTMLTableSectionElement>) => <tbody className="[&_tr:last-child]:border-0" {...props} />;
const TableRow = ({ className, ...props }: React.HTMLAttributes<HTMLTableRowElement>) => (
  <tr className={cn("border-b border-line transition-colors hover:bg-sunken/60", className)} {...props} />
);
const TableHead = ({ className, ...props }: React.ThHTMLAttributes<HTMLTableCellElement>) => (
  <th scope="col" className={cn("sticky top-0 h-9 whitespace-nowrap px-3 text-left align-middle text-xs font-bold lowercase tracking-wide text-ink-soft", className)} {...props} />
);
const TableCell = ({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) => (
  <td className={cn("px-3 py-2 align-middle", className)} {...props} />
);

export { Table, TableBody, TableCell, TableHead, TableHeader, TableRow };

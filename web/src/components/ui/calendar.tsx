"use client";

import * as React from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { DayPicker } from "react-day-picker";

import { cn } from "@/lib/utils";

function Calendar({
  className,
  classNames,
  showOutsideDays = true,
  ...props
}: React.ComponentProps<typeof DayPicker>) {
  return (
    <DayPicker
      showOutsideDays={showOutsideDays}
      className={cn("relative p-1 text-sm", className)}
      classNames={{
        months: "relative flex flex-col gap-4 sm:flex-row",
        month: "min-w-0",
        month_caption: "flex h-10 items-center justify-center px-10 font-medium",
        caption_label: "truncate text-sm font-semibold",
        nav: "pointer-events-none absolute inset-x-1 top-1 z-10 flex items-center justify-between",
        button_previous: "pointer-events-auto inline-flex size-8 items-center justify-center rounded-full bg-background/95 text-foreground shadow-sm ring-1 ring-border transition hover:bg-accent disabled:opacity-40 dark:bg-background/80 dark:text-foreground dark:ring-border dark:hover:bg-accent/70",
        button_next: "pointer-events-auto inline-flex size-8 items-center justify-center rounded-full bg-background/95 text-foreground shadow-sm ring-1 ring-border transition hover:bg-accent disabled:opacity-40 dark:bg-background/80 dark:text-foreground dark:ring-border dark:hover:bg-accent/70",
        month_grid: "w-full border-separate border-spacing-y-1",
        weekdays: "mt-2 grid grid-cols-7 gap-x-1 text-xs text-muted-foreground",
        weekday: "flex h-8 items-center justify-center font-normal",
        week: "grid grid-cols-7 gap-x-1",
        day: "h-9 p-0 text-center align-middle text-sm",
        day_button: "mx-auto inline-flex size-8 items-center justify-center rounded-lg text-sm font-medium text-foreground transition hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/70 dark:hover:text-foreground",
        today: "font-semibold text-foreground",
        selected: "font-semibold [&_button]:bg-primary [&_button]:text-primary-foreground [&_button]:hover:bg-primary/90 dark:[&_button]:bg-sky-400 dark:[&_button]:text-sky-950 dark:[&_button]:hover:bg-sky-300",
        range_start: "rdp-range_start",
        range_middle: "rdp-range_middle",
        range_end: "rdp-range_end",
        outside: "text-muted-foreground/45 dark:text-muted-foreground/35",
        disabled: "text-muted-foreground/35 opacity-50 dark:text-muted-foreground/30",
        ...classNames,
      }}
      components={{
        Chevron: ({ orientation }) =>
          orientation === "left" ? <ChevronLeft className="size-4" /> : <ChevronRight className="size-4" />,
      }}
      {...props}
    />
  );
}

export { Calendar };

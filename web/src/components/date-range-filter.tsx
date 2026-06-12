"use client";

import { useEffect, useState } from "react";
import { format, parse } from "date-fns";
import { CalendarIcon } from "lucide-react";
import type { DateRange } from "react-day-picker";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { Field } from "@/components/ui/field";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

type DateRangeFilterProps = {
  startDate: string;
  endDate: string;
  onChange: (startDate: string, endDate: string) => void;
  className?: string;
};

export function DateRangeFilter({ startDate, endDate, onChange, className }: DateRangeFilterProps) {
  const [numberOfMonths, setNumberOfMonths] = useState(1);
  const selected: DateRange | undefined = startDate
    ? {
        from: parse(startDate, "yyyy-MM-dd", new Date()),
        to: endDate ? parse(endDate, "yyyy-MM-dd", new Date()) : undefined,
      }
    : undefined;

  const label = startDate ? `${startDate} 至 ${endDate || startDate}` : "选择日期范围";

  useEffect(() => {
    const query = window.matchMedia("(min-width: 640px)");
    const updateNumberOfMonths = () => setNumberOfMonths(query.matches ? 2 : 1);

    updateNumberOfMonths();
    query.addEventListener("change", updateNumberOfMonths);
    return () => query.removeEventListener("change", updateNumberOfMonths);
  }, []);

  return (
    <Field className={cn("w-full sm:w-[240px]", className)}>
      <Popover>
        <PopoverTrigger asChild>
          <Button
            variant="outline"
            className="h-10 w-full min-w-0 justify-start rounded-lg px-3 font-normal dark:border-border dark:bg-background/70 dark:text-foreground dark:hover:bg-accent/60"
          >
            <CalendarIcon className="size-4 shrink-0 text-muted-foreground dark:text-muted-foreground" />
            <span className="truncate">{label}</span>
          </Button>
        </PopoverTrigger>
        <PopoverContent
          className="w-[min(calc(100vw-1.5rem),20.5rem)] border-border bg-popover p-2 shadow-[0_20px_60px_-30px_rgba(15,23,42,0.35)] dark:border-border dark:bg-card dark:shadow-[0_24px_80px_-28px_rgba(0,0,0,0.72)] sm:w-auto sm:max-w-none sm:p-3"
          align="center"
          collisionPadding={12}
          sideOffset={8}
        >
          <Calendar
            mode="range"
            defaultMonth={selected?.from}
            selected={selected}
            onSelect={(value) => onChange(value?.from ? format(value.from, "yyyy-MM-dd") : "", value?.to ? format(value.to, "yyyy-MM-dd") : "")}
            numberOfMonths={numberOfMonths}
          />
        </PopoverContent>
      </Popover>
    </Field>
  );
}

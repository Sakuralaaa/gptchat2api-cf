"use client";

import { Import, LoaderCircle, Search } from "lucide-react";
import { useMemo } from "react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import { PAGE_SIZE_OPTIONS, useSettingsStore } from "../store";
import { settingsDialogInputClassName } from "./settings-ui";

export function ImportBrowserDialog() {
  const browserOpen = useSettingsStore((state) => state.browserOpen);
  const browserPool = useSettingsStore((state) => state.browserPool);
  const remoteFiles = useSettingsStore((state) => state.remoteFiles);
  const selectedNames = useSettingsStore((state) => state.selectedNames);
  const fileQuery = useSettingsStore((state) => state.fileQuery);
  const filePage = useSettingsStore((state) => state.filePage);
  const pageSize = useSettingsStore((state) => state.pageSize);
  const isStartingImport = useSettingsStore((state) => state.isStartingImport);
  const setBrowserOpen = useSettingsStore((state) => state.setBrowserOpen);
  const toggleFile = useSettingsStore((state) => state.toggleFile);
  const replaceSelectedNames = useSettingsStore(
    (state) => state.replaceSelectedNames,
  );
  const setFileQuery = useSettingsStore((state) => state.setFileQuery);
  const setFilePage = useSettingsStore((state) => state.setFilePage);
  const setPageSize = useSettingsStore((state) => state.setPageSize);
  const startImport = useSettingsStore((state) => state.startImport);

  const filteredFiles = useMemo(() => {
    const query = fileQuery.trim().toLowerCase();
    if (!query) {
      return remoteFiles;
    }
    return remoteFiles.filter((item) => {
      return (
        item.email.toLowerCase().includes(query) ||
        item.name.toLowerCase().includes(query)
      );
    });
  }, [fileQuery, remoteFiles]);

  const currentPageSize = Number(pageSize);
  const filePageCount = Math.max(
    1,
    Math.ceil(filteredFiles.length / currentPageSize),
  );
  const safeFilePage = Math.min(filePage, filePageCount);
  const pagedFiles = filteredFiles.slice(
    (safeFilePage - 1) * currentPageSize,
    safeFilePage * currentPageSize,
  );
  const allFilteredSelected =
    filteredFiles.length > 0 &&
    filteredFiles.every((item) => selectedNames.includes(item.name));

  const toggleSelectAllFiltered = (checked: boolean) => {
    if (checked) {
      replaceSelectedNames([
        ...selectedNames,
        ...filteredFiles.map((item) => item.name),
      ]);
      return;
    }
    const filteredSet = new Set(filteredFiles.map((item) => item.name));
    replaceSelectedNames(
      selectedNames.filter((name) => !filteredSet.has(name)),
    );
  };

  return (
    <Dialog open={browserOpen} onOpenChange={setBrowserOpen}>
      <DialogContent
        showCloseButton={false}
        className="max-h-[90vh] max-w-5xl rounded-2xl p-6"
      >
        <DialogHeader className="gap-2">
          <DialogTitle>选择要导入的账号</DialogTitle>
          <DialogDescription className="text-sm leading-6">
            {browserPool
              ? `来自 ${browserPool.name || browserPool.base_url}`
              : "读取到的远程账号列表"}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div className="relative min-w-[260px]">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={fileQuery}
              onChange={(event) => setFileQuery(event.target.value)}
              placeholder="搜索 email 或文件名"
              className={`${settingsDialogInputClassName} pl-10`}
            />
          </div>
          <div className="flex items-center gap-2">
            <Select
              value={pageSize}
              onValueChange={(value) =>
                setPageSize(value as (typeof PAGE_SIZE_OPTIONS)[number])
              }
            >
              <SelectTrigger className="h-11 w-[120px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {PAGE_SIZE_OPTIONS.map((item) => (
                    <SelectItem key={item} value={item}>
                      {item} / 页
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button
              variant="outline"
              size="lg"
              onClick={() => toggleSelectAllFiltered(!allFilteredSelected)}
            >
              {allFilteredSelected ? "取消全选" : "全选筛选结果"}
            </Button>
          </div>
        </div>

        <div className="rounded-[16px] border border-border/80">
          <div className="flex items-center justify-between border-b border-[#f2f3f5] px-4 py-3 text-sm text-muted-foreground">
            <div className="flex items-center gap-3">
              <Checkbox
                checked={allFilteredSelected}
                onCheckedChange={(checked) =>
                  toggleSelectAllFiltered(Boolean(checked))
                }
              />
              <span>筛选结果 {filteredFiles.length} 个</span>
            </div>
            <span>已选 {selectedNames.length} 个</span>
          </div>
          <div className="max-h-[420px] overflow-auto">
            {pagedFiles.length === 0 ? (
              <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
                没有匹配的远程账号
              </div>
            ) : (
              <div className="divide-y divide-[#f2f3f5]">
                {pagedFiles.map((item) => (
                  <label
                    key={item.name}
                    className="flex cursor-pointer items-center gap-3 px-4 py-3 hover:bg-muted/60"
                  >
                    <Checkbox
                      checked={selectedNames.includes(item.name)}
                      onCheckedChange={(checked) =>
                        toggleFile(item.name, Boolean(checked))
                      }
                    />
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium text-foreground">
                        {item.email || item.name}
                      </div>
                      <div className="truncate text-xs text-muted-foreground">
                        {item.name}
                      </div>
                    </div>
                  </label>
                ))}
              </div>
            )}
          </div>
        </div>

        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>
            第{" "}
            {filteredFiles.length === 0
              ? 0
              : (safeFilePage - 1) * currentPageSize + 1}{" "}
            - {Math.min(safeFilePage * currentPageSize, filteredFiles.length)}{" "}
            条，共 {filteredFiles.length} 条
          </span>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => setFilePage(Math.max(1, safeFilePage - 1))}
              disabled={safeFilePage <= 1}
            >
              上一页
            </Button>
            <span>
              {safeFilePage}/{filePageCount}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                setFilePage(Math.min(filePageCount, safeFilePage + 1))
              }
              disabled={safeFilePage >= filePageCount}
            >
              下一页
            </Button>
          </div>
        </div>

        <DialogFooter className="pt-2">
          <Button
            variant="secondary"
            size="lg"
            onClick={() => setBrowserOpen(false)}
            disabled={isStartingImport}
          >
            取消
          </Button>
          <Button
            size="lg"
            onClick={() => void startImport()}
            disabled={isStartingImport || selectedNames.length === 0}
          >
            {isStartingImport ? (
              <LoaderCircle data-icon="inline-start" className="animate-spin" />
            ) : (
              <Import data-icon="inline-start" />
            )}
            导入选中账号
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

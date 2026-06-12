"use client";

export type ImageTurnProgress = {
  message: string;
  detail?: string;
  startedAt: number;
};

export const IMAGE_TURN_PROGRESS_CHANGED_EVENT = "chatgpt2api:image-turn-progress-changed";

let imageTurnProgressByKey: Record<string, ImageTurnProgress> = {};

export function imageTurnProgressKey(conversationId: string, turnId: string) {
  return `${conversationId}:${turnId}`;
}

export function imageTurnStartedAtTimestamp(processingStartedAt: string | undefined, createdAt: string) {
  const timestamp = Date.parse(processingStartedAt || createdAt);
  return Number.isFinite(timestamp) ? timestamp : Date.now();
}

export function getImageTurnProgressSnapshot() {
  return imageTurnProgressByKey;
}

function dispatchImageTurnProgressChanged() {
  if (typeof window === "undefined") {
    return;
  }
  window.dispatchEvent(new Event(IMAGE_TURN_PROGRESS_CHANGED_EVENT));
}

export function subscribeImageTurnProgress(listener: () => void) {
  if (typeof window === "undefined") {
    return () => undefined;
  }

  window.addEventListener(IMAGE_TURN_PROGRESS_CHANGED_EVENT, listener);
  return () => {
    window.removeEventListener(IMAGE_TURN_PROGRESS_CHANGED_EVENT, listener);
  };
}

export function setImageTurnProgress(
  conversationId: string,
  turnId: string,
  updates: Omit<ImageTurnProgress, "startedAt"> & { startedAt?: number },
) {
  const key = imageTurnProgressKey(conversationId, turnId);
  imageTurnProgressByKey = {
    ...imageTurnProgressByKey,
    [key]: {
      ...updates,
      startedAt: updates.startedAt ?? imageTurnProgressByKey[key]?.startedAt ?? Date.now(),
    },
  };
  dispatchImageTurnProgressChanged();
}

export function clearImageTurnProgress(conversationId: string, turnId: string) {
  const key = imageTurnProgressKey(conversationId, turnId);
  if (!imageTurnProgressByKey[key]) {
    return;
  }

  const nextProgress = { ...imageTurnProgressByKey };
  delete nextProgress[key];
  imageTurnProgressByKey = nextProgress;
  dispatchImageTurnProgressChanged();
}

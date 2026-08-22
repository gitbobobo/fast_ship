const positions = new Map<string, number>();

export function getSavedScroll(key: string) {
  return positions.get(key) ?? 0;
}

export function setSavedScroll(key: string, value: number) {
  positions.set(key, value);
}

export function getLocationScrollKey(pathname: string, search = "") {
  return `main:${pathname}${search}`;
}

export function resetScrollPositions() {
  positions.clear();
}

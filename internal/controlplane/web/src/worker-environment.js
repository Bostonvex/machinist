export function workerEnvironmentLabel(environment) {
  if (!environment?.os || !environment?.arch) return "Environment unavailable";
  return [
    `${environment.os}/${environment.arch}`,
    environment.execution,
    environment.shell,
  ].filter(Boolean).join(" · ");
}

export function sortedProfiles(profiles) {
  return Object.entries(profiles || {}).sort(([left], [right]) => left.localeCompare(right));
}


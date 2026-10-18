import { useEffect, useState } from "react";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import { Button } from "./primitives/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "./primitives/dropdown-menu";

export type ThemeName = "light" | "dark";
export type ThemePreference = ThemeName | "system";

export function readThemePreference(storageKey: string): ThemePreference {
  const value = localStorage.getItem(storageKey);
  return value === "light" || value === "dark" ? value : "system";
}

export function usePortalTheme(storageKey: string) {
  const [preference, setPreferenceState] = useState(() => readThemePreference(storageKey));
  const [systemDark, setSystemDark] = useState(() => window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false);
  const theme: ThemeName = preference === "system" ? systemDark ? "dark" : "light" : preference;

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const update = () => setSystemDark(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    document.documentElement.dataset.theme = theme;
    document.documentElement.style.colorScheme = theme;
  }, [theme]);

  function setPreference(next: ThemePreference) {
    localStorage.setItem(storageKey, next);
    setPreferenceState(next);
  }

  return { theme, preference, setPreference };
}

export function ThemeControl({ preference, onChange }: { preference: ThemePreference; onChange: (theme: ThemePreference) => void }) {
  const options = [
    { value: "system", label: "System", icon: Monitor },
    { value: "light", label: "Light", icon: Sun },
    { value: "dark", label: "Dark", icon: Moon },
  ] as const;
  const Icon = options.find((option) => option.value === preference)!.icon;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild><Button variant="ghost" size="icon" aria-label="Choose theme" title="Choose theme"><Icon aria-hidden="true" /></Button></DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Appearance</DropdownMenuLabel>
        {options.map((option) => <DropdownMenuItem key={option.value} onSelect={() => onChange(option.value)}><option.icon aria-hidden="true" /><span>{option.label}</span>{preference === option.value && <Check className="ml-auto" aria-label="Selected" />}</DropdownMenuItem>)}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

import { languages, setLanguagePreference, t } from "./i18n.js";

// Local presentation only. Native radios provide arrow-key navigation.
export function initializeLanguageMenu() {
  const toggle = document.querySelector("#language-toggle");
  const popup = document.querySelector("#language-popup");
  if (!toggle || !popup) return;
  for (const language of [{code: "system", label: ""}, ...languages]) {
    const label = document.createElement("label");
    const radio = document.createElement("input");
    radio.type = "radio";
    radio.name = "filees-language";
    radio.value = language.code;
    const text = document.createElement("span");
    if (language.code === "system") text.dataset.i18n = "language.system";
    text.textContent = language.code === "system" ? t("language.system") : language.label;
    label.append(radio, text);
    popup.append(label);
  }
  const refresh = () => {
    const selected = document.documentElement.dataset.languagePreference || "system";
    popup.querySelectorAll("input").forEach(radio => { radio.checked = radio.value === selected; });
  };
  const close = (focus = false) => {
    popup.hidden = true;
    toggle.setAttribute("aria-expanded", "false");
    if (focus) toggle.focus();
  };
  const open = () => {
    refresh();
    popup.hidden = false;
    toggle.setAttribute("aria-expanded", "true");
    popup.querySelector("input:checked")?.focus();
  };
  toggle.addEventListener("click", () => popup.hidden ? open() : close(true));
  toggle.addEventListener("keydown", event => {
    if (event.key === "ArrowDown") { event.preventDefault(); open(); }
  });
  popup.addEventListener("change", event => {
    if (event.target.matches("input[type=radio]")) setLanguagePreference(event.target.value);
  });
  // Selecting by pointer closes the popup; arrow keys keep the radio group open.
  popup.addEventListener("click", event => {
    if (event.detail > 0 && event.target.matches("input[type=radio]")) close(true);
  });
  document.addEventListener("keydown", event => {
    if (!popup.hidden && event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(true); }
  }, true);
  document.addEventListener("pointerdown", event => {
    if (!popup.contains(event.target) && !toggle.contains(event.target)) close();
  });
  document.addEventListener("focusin", event => {
    if (!popup.contains(event.target) && !toggle.contains(event.target)) close();
  });
  window.addEventListener("filees:language-changed", refresh);
  refresh();
}

import { Component, type OnInit } from "@angular/core";
import { type Settings, ApiService } from "../../services/api.service";

@Component({
  selector: "app-settings",
  standalone: true,
  template: `
    <div class="settings">
      <h2>Settings</h2>
      @if (settings) {
        <div class="setting-row">
          <span class="label">Web Dashboard</span>
          <span class="value on">Active</span>
        </div>
        <div class="setting-row">
          <span class="label">Telegram Bot</span>
          @if (settings.telegram_connected) {
            <span class="value on">Connected</span>
          } @else if (settings.telegram_configured) {
            <span class="value warn">Configured but not connected</span>
          } @else {
            <span class="value off">Not configured</span>
          }
        </div>
      } @else if (error) {
        <p class="error">{{ error }}</p>
      } @else {
        <p class="loading">Loading...</p>
      }
    </div>
  `,
  styles: [`
    .settings { padding: 8px 0; }
    h2 { margin: 0 0 12px; font-size: 16px; }
    .setting-row { display: flex; justify-content: space-between; align-items: center; padding: 10px; border: 1px solid var(--border); border-radius: 6px; margin-bottom: 6px; }
    .label { font-weight: 600; font-size: 13px; }
    .value { font-size: 13px; padding: 2px 8px; border-radius: 4px; }
    .on { color: #22c55e; }
    .warn { color: #eab308; }
    .off { color: #94a3b8; }
    .error { color: #ef4444; }
    .loading { color: var(--fg-muted); }
  `],
})
export class SettingsComponent implements OnInit {
  settings: Settings | null = null;
  error = "";

  constructor(private api: ApiService) {}

  async ngOnInit() {
    try {
      this.settings = await this.api.getSettings();
    } catch (e) {
      this.error = "Failed to load settings";
    }
  }
}

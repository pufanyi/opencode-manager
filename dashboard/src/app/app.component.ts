import { Component, type OnDestroy, type OnInit } from "@angular/core";
import { type Instance, ApiService } from "./services/api.service";
import { InstanceListComponent } from "./components/instance-list/instance-list.component";
import { PromptPanelComponent } from "./components/prompt-panel/prompt-panel.component";
import { SettingsComponent } from "./components/settings/settings.component";

@Component({
  selector: "app-root",
  standalone: true,
  imports: [InstanceListComponent, PromptPanelComponent, SettingsComponent],
  template: `
    <div class="app">
      <header>
        <h1>OpenCode Manager</h1>
        <nav>
          <button [class.active]="tab === 'instances'" (click)="tab = 'instances'">Instances</button>
          <button [class.active]="tab === 'settings'" (click)="tab = 'settings'">Settings</button>
        </nav>
      </header>

      <main>
        @if (tab === 'instances') {
          <div class="layout">
            <aside>
              <app-instance-list
                [instances]="instances"
                [selected]="selectedInstance"
                (select)="onSelect($event)"
                (changed)="refresh()"
              />
            </aside>
            <section class="content">
              <app-prompt-panel [instance]="selectedInstance" />
            </section>
          </div>
        }

        @if (tab === 'settings') {
          <app-settings />
        }
      </main>
    </div>
  `,
  styles: [`
    :host { display: block; height: 100vh; }
    .app { display: flex; flex-direction: column; height: 100%; }
    header { display: flex; justify-content: space-between; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--border); }
    header h1 { margin: 0; font-size: 18px; font-weight: 700; }
    nav { display: flex; gap: 4px; }
    nav button { padding: 6px 12px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--fg); cursor: pointer; font-size: 13px; }
    nav button.active { background: var(--accent); color: white; border-color: var(--accent); }
    main { flex: 1; padding: 16px; overflow: hidden; }
    .layout { display: flex; gap: 16px; height: 100%; }
    aside { width: 280px; flex-shrink: 0; overflow-y: auto; }
    .content { flex: 1; min-width: 0; }
  `],
})
export class AppComponent implements OnInit, OnDestroy {
  instances: Instance[] = [];
  selectedInstance: Instance | null = null;
  tab: "instances" | "settings" = "instances";
  private refreshTimer: ReturnType<typeof setInterval> | null = null;

  constructor(private api: ApiService) {}

  ngOnInit() {
    this.refresh();
    // Poll instance list every 5s (lightweight, direct HTTP).
    this.refreshTimer = setInterval(() => this.refresh(), 5000);
  }

  ngOnDestroy() {
    if (this.refreshTimer) clearInterval(this.refreshTimer);
  }

  async refresh() {
    try {
      this.instances = await this.api.getInstances();
      if (this.selectedInstance) {
        const updated = this.instances.find((i) => i.id === this.selectedInstance!.id);
        if (updated) {
          this.selectedInstance = updated;
        } else {
          this.selectedInstance = null;
        }
      }
    } catch {
      // server might not be ready yet
    }
  }

  onSelect(instance: Instance) {
    this.selectedInstance = instance;
  }
}

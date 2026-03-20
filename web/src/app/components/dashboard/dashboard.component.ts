import { Component, type OnDestroy, type OnInit } from "@angular/core";
import { FormsModule } from "@angular/forms";
import type { Unsubscribe } from "firebase/database";
import { FirebaseService, type Instance } from "../../services/firebase.service";
import { InstanceCardComponent } from "../instance-card/instance-card.component";
import { PromptPanelComponent } from "../prompt-panel/prompt-panel.component";

@Component({
  selector: "app-dashboard",
  standalone: true,
  imports: [FormsModule, InstanceCardComponent, PromptPanelComponent],
  templateUrl: "./dashboard.component.html",
  styleUrl: "./dashboard.component.scss",
})
export class DashboardComponent implements OnInit, OnDestroy {
  instances: Instance[] = [];
  selectedInstance: Instance | null = null;
  showNewForm = false;
  newName = "";
  newDirectory = "";
  newProvider = "claudecode";

  private unsubInstances: Unsubscribe | null = null;

  constructor(private firebase: FirebaseService) {}

  ngOnInit() {
    this.unsubInstances = this.firebase.onInstances((instances) => {
      this.instances = instances;
      if (this.selectedInstance) {
        const updated = instances.find((i) => i.id === this.selectedInstance!.id);
        this.selectedInstance = updated || null;
      }
    });
  }

  ngOnDestroy() {
    this.unsubInstances?.();
  }

  toggleNewForm(): void {
    this.showNewForm = !this.showNewForm;
    if (!this.showNewForm) {
      this.newName = "";
      this.newDirectory = "";
      this.newProvider = "claudecode";
    }
  }

  async createInstance() {
    if (!this.newName.trim() || !this.newDirectory.trim()) return;
    try {
      await this.firebase.sendCommandAndWait("_system", "create", {
        name: this.newName.trim(),
        directory: this.newDirectory.trim(),
        provider: this.newProvider,
      });
      this.showNewForm = false;
      this.newName = "";
      this.newDirectory = "";
    } catch (e) {
      console.error("Create failed:", e);
    }
  }

  onSelect(instance: Instance): void {
    this.selectedInstance = instance;
  }

  async onStart(instance: Instance) {
    await this.firebase.sendCommand(instance.id, "start");
  }

  async onStop(instance: Instance) {
    await this.firebase.sendCommand(instance.id, "stop");
  }

  async onDelete(instance: Instance) {
    if (this.selectedInstance?.id === instance.id) {
      this.selectedInstance = null;
    }
    await this.firebase.sendCommand(instance.id, "delete");
  }

  logout() {
    this.firebase.logout();
  }
}

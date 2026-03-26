import { Component, type OnDestroy, type OnInit } from "@angular/core";
import { FormsModule } from "@angular/forms";
import type { Unsubscribe } from "firebase/database";
import type { Subscription } from "rxjs";
import { filter, take } from "rxjs";
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

  isLinked: boolean | null = null;
  linkCode: string | null = null;
  showLinkSection = false;

  private uid: string | null = null;
  private unsubLinkStatus: Unsubscribe | null = null;
  private instancePollTimer: ReturnType<typeof setInterval> | null = null;
  private userSub: Subscription | null = null;

  constructor(private firebase: FirebaseService) {}

  ngOnInit() {
    // Wait for Firebase auth to resolve before setting up listeners
    this.userSub = this.firebase.user$
      .pipe(
        filter((u) => u !== null), // skip "still loading"
        take(1),
      )
      .subscribe((user) => {
        if (!user) return; // false = no user, guard will redirect
        this.uid = user.uid;

        // Always start loading instances immediately
        this.loadInstances();
        this.instancePollTimer = setInterval(() => this.loadInstances(), 5000);

        // Check Telegram link status in the background (optional feature)
        this.unsubLinkStatus = this.firebase.onUserLinkStatus(user.uid, (isLinked) => {
          this.isLinked = isLinked;
        });
      });
  }

  async generateLinkCode() {
    if (!this.uid || this.linkCode) return;
    try {
      this.linkCode = await this.firebase.generateLinkCode(this.uid);
    } catch (e) {
      console.error("Failed to generate link code", e);
    }
  }

  toggleLinkSection() {
    this.showLinkSection = !this.showLinkSection;
    if (this.showLinkSection && !this.linkCode && !this.isLinked) {
      this.generateLinkCode();
    }
  }

  ngOnDestroy() {
    this.userSub?.unsubscribe();
    this.unsubLinkStatus?.();
    if (this.instancePollTimer) {
      clearInterval(this.instancePollTimer);
    }
  }

  private async loadInstances() {
    if (!this.uid) return;
    try {
      const instances = await this.firebase.getInstances(this.uid);
      this.instances = instances;
      if (this.selectedInstance) {
        const updated = instances.find((i) => i.id === this.selectedInstance!.id);
        this.selectedInstance = updated || null;
      }
    } catch (e) {
      console.error("Failed to load instances", e);
    }
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
    if (!this.newName.trim() || !this.newDirectory.trim() || !this.uid) return;
    try {
      await this.firebase.sendCommandAndWait(this.uid, "_system", "create", {
        name: this.newName.trim(),
        directory: this.newDirectory.trim(),
        provider: this.newProvider,
      });
      this.showNewForm = false;
      this.newName = "";
      this.newDirectory = "";
      await this.loadInstances();
    } catch (e) {
      console.error("Create failed:", e);
    }
  }

  onSelect(instance: Instance): void {
    this.selectedInstance = instance;
  }

  async onStart(instance: Instance) {
    if (!this.uid) return;
    await this.firebase.sendCommand(this.uid, instance.id, "start");
  }

  async onStop(instance: Instance) {
    if (!this.uid) return;
    await this.firebase.sendCommand(this.uid, instance.id, "stop");
  }

  async onDelete(instance: Instance) {
    if (!this.uid) return;
    if (this.selectedInstance?.id === instance.id) {
      this.selectedInstance = null;
    }
    await this.firebase.sendCommand(this.uid, instance.id, "delete");
    await this.loadInstances();
  }

  logout() {
    this.firebase.logout();
  }
}

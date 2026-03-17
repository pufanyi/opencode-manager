import { Component, OnDestroy, OnInit, inject } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { Subscription } from 'rxjs';
import { ApiService, Instance } from '../../services/api.service';
import { InstanceCardComponent } from '../instance-card/instance-card.component';
import { PromptPanelComponent } from '../prompt-panel/prompt-panel.component';

@Component({
  selector: 'app-dashboard',
  imports: [FormsModule, InstanceCardComponent, PromptPanelComponent],
  templateUrl: './dashboard.component.html',
  styleUrl: './dashboard.component.scss',
})
export class DashboardComponent implements OnInit, OnDestroy {
  private api = inject(ApiService);

  instances: Instance[] = [];
  selectedInstance: Instance | null = null;
  showNewForm = false;

  newName = '';
  newDirectory = '';
  newProvider = 'claudecode';

  private sub!: Subscription;

  ngOnInit(): void {
    this.sub = this.api.instances$.subscribe((list) => {
      this.instances = list;
      // Keep selected instance reference in sync
      if (this.selectedInstance) {
        const updated = list.find((i) => i.id === this.selectedInstance!.id);
        if (updated) {
          this.selectedInstance = updated;
        }
      }
    });
  }

  ngOnDestroy(): void {
    this.sub.unsubscribe();
  }

  toggleNewForm(): void {
    this.showNewForm = !this.showNewForm;
    if (!this.showNewForm) {
      this.resetForm();
    }
  }

  createInstance(): void {
    if (!this.newName.trim() || !this.newDirectory.trim()) return;
    this.api
      .createInstance({
        name: this.newName.trim(),
        directory: this.newDirectory.trim(),
        provider_type: this.newProvider,
      })
      .subscribe({
        next: () => {
          this.api.refreshInstances();
          this.showNewForm = false;
          this.resetForm();
        },
      });
  }

  onSelect(instance: Instance): void {
    this.selectedInstance = instance;
  }

  onStart(instance: Instance): void {
    this.api.startInstance(instance.id).subscribe({
      next: () => this.api.refreshInstances(),
    });
  }

  onStop(instance: Instance): void {
    this.api.stopInstance(instance.id).subscribe({
      next: () => this.api.refreshInstances(),
    });
  }

  onDelete(instance: Instance): void {
    this.api.deleteInstance(instance.id).subscribe({
      next: () => {
        if (this.selectedInstance?.id === instance.id) {
          this.selectedInstance = null;
        }
        this.api.refreshInstances();
      },
    });
  }

  private resetForm(): void {
    this.newName = '';
    this.newDirectory = '';
    this.newProvider = 'claudecode';
  }
}

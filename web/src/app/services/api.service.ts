import { Injectable, NgZone, OnDestroy } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { BehaviorSubject, Observable, Subscription, timer } from 'rxjs';
import { switchMap } from 'rxjs/operators';

export interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  port?: number;
}

export interface Session {
  ID: string;
  Title: string;
}

export interface StreamEvent {
  session_id: string;
  event: {
    Type: string;
    Text?: string;
    ToolName?: string;
    ToolState?: string;
    Done?: boolean;
    Error?: string;
  };
}

@Injectable({
  providedIn: 'root',
})
export class ApiService implements OnDestroy {
  private readonly baseUrl = '/api';
  private readonly pollSub: Subscription;

  readonly instances$ = new BehaviorSubject<Instance[]>([]);

  constructor(
    private http: HttpClient,
    private zone: NgZone,
  ) {
    this.pollSub = timer(0, 5000)
      .pipe(switchMap(() => this.http.get<Instance[]>(`${this.baseUrl}/instances`)))
      .subscribe({
        next: (instances) => this.instances$.next(instances),
        error: () => {},
      });
  }

  ngOnDestroy(): void {
    this.pollSub.unsubscribe();
  }

  refreshInstances(): void {
    this.http.get<Instance[]>(`${this.baseUrl}/instances`).subscribe({
      next: (instances) => this.instances$.next(instances),
    });
  }

  createInstance(body: {
    name: string;
    directory: string;
    provider_type: string;
  }): Observable<Instance> {
    return this.http.post<Instance>(`${this.baseUrl}/instances`, body);
  }

  startInstance(id: string): Observable<unknown> {
    return this.http.post(`${this.baseUrl}/instances/${id}/start`, {});
  }

  stopInstance(id: string): Observable<unknown> {
    return this.http.post(`${this.baseUrl}/instances/${id}/stop`, {});
  }

  deleteInstance(id: string): Observable<unknown> {
    return this.http.post(`${this.baseUrl}/instances/${id}/delete`, {});
  }

  getSessions(instanceId: string): Observable<Session[]> {
    return this.http.get<Session[]>(
      `${this.baseUrl}/instances/${instanceId}/sessions`,
    );
  }

  createSession(instanceId: string): Observable<Session> {
    return this.http.post<Session>(
      `${this.baseUrl}/sessions/${instanceId}/new`,
      {},
    );
  }

  sendPrompt(body: {
    instance_id: string;
    session_id: string;
    content: string;
  }): Observable<unknown> {
    return this.http.post(`${this.baseUrl}/prompt`, body);
  }

  abortPrompt(body: {
    instance_id: string;
    session_id: string;
  }): Observable<unknown> {
    return this.http.post(`${this.baseUrl}/abort`, body);
  }

  connectSSE(sessionId: string): EventSource {
    const url = `${this.baseUrl}/ws?session=${encodeURIComponent(sessionId)}`;
    return new EventSource(url);
  }
}

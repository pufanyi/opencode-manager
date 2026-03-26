import { Injectable, NgZone } from "@angular/core";
import { type FirebaseApp, initializeApp } from "firebase/app";
import {
  type Auth,
  createUserWithEmailAndPassword,
  GoogleAuthProvider,
  getAuth,
  onAuthStateChanged,
  signInWithEmailAndPassword,
  signInWithPopup,
  signOut,
  type User,
} from "firebase/auth";
import {
  type Database,
  getDatabase,
  onValue,
  push,
  ref,
  remove,
  set,
  type Unsubscribe,
} from "firebase/database";
import {
  collection,
  type Firestore,
  getDocs,
  getFirestore,
  onSnapshot,
  orderBy,
  query,
} from "firebase/firestore";
import { BehaviorSubject, type Observable } from "rxjs";
import { environment } from "../../environments/environment";

export interface Instance {
  id: string;
  name: string;
  directory: string;
  status: string;
  provider_type: string;
  client_id?: string;
}

export interface InstanceRuntime {
  online: boolean;
  client_id: string;
  last_seen: number;
}

export interface StreamData {
  content: string;
  status: string;
  tool_calls: { name: string; status: string; detail: string }[];
  error?: string;
  updated_at: number;
}

export interface Command {
  action: string;
  payload: unknown;
  status: string;
  user_id: string;
  created_at: number;
  updated_at: number;
  result?: unknown;
  error?: string;
}

export interface HistoryMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  tool_calls: { name: string; status: string; detail: string; input?: string; output?: string }[];
  created_at: string;
}

@Injectable({ providedIn: "root" })
export class FirebaseService {
  private app: FirebaseApp;
  private auth: Auth;
  private db: Database;
  private firestore: Firestore;

  // null = not yet checked, User | false = resolved
  private userSubject = new BehaviorSubject<User | null | false>(null);
  user$: Observable<User | null | false> = this.userSubject.asObservable();

  constructor(private zone: NgZone) {
    this.app = initializeApp(environment.firebase);
    this.auth = getAuth(this.app);
    this.db = getDatabase(this.app);
    this.firestore = getFirestore(this.app);

    onAuthStateChanged(this.auth, (user) => {
      this.zone.run(() => this.userSubject.next(user ?? false));
    });
  }

  get currentUser(): User | null {
    return this.auth.currentUser;
  }

  // -- Auth --

  async login(email: string, password: string): Promise<void> {
    await signInWithEmailAndPassword(this.auth, email, password);
  }

  async register(email: string, password: string): Promise<void> {
    await createUserWithEmailAndPassword(this.auth, email, password);
  }

  async loginWithGoogle(): Promise<void> {
    await signInWithPopup(this.auth, new GoogleAuthProvider());
  }

  async logout(): Promise<void> {
    await signOut(this.auth);
  }

  // -- Firestore: Instance List (user-scoped) --

  async getInstances(uid: string): Promise<Instance[]> {
    const instancesRef = collection(this.firestore, "users", uid, "instances");
    const snapshot = await getDocs(instancesRef);
    const instances: Instance[] = snapshot.docs.map((d) => {
      const data = d.data();
      return {
        id: data.id || d.id,
        name: data.name || "",
        directory: data.directory || "",
        status: data.status || "stopped",
        provider_type: data.provider_type || "claudecode",
        client_id: data.client_id || "",
      } as Instance;
    });
    instances.sort((a, b) => a.name.localeCompare(b.name));
    return instances;
  }

  // -- Firestore: Real-time Instance List (user-scoped) --

  onInstances(uid: string, callback: (instances: Instance[]) => void): () => void {
    const instancesRef = collection(this.firestore, "users", uid, "instances");
    return onSnapshot(instancesRef, (snapshot) => {
      const instances: Instance[] = snapshot.docs.map((d) => {
        const data = d.data();
        return {
          id: data.id || d.id,
          name: data.name || "",
          directory: data.directory || "",
          status: data.status || "stopped",
          provider_type: data.provider_type || "claudecode",
          client_id: data.client_id || "",
        } as Instance;
      });
      instances.sort((a, b) => a.name.localeCompare(b.name));
      this.zone.run(() => callback(instances));
    });
  }

  // -- RTDB: Instance Runtime (user-scoped presence) --

  onInstanceRuntime(
    uid: string,
    instanceId: string,
    callback: (runtime: InstanceRuntime | null) => void,
  ): Unsubscribe {
    const dbRef = ref(this.db, `users/${uid}/instances/${instanceId}/runtime`);
    return onValue(dbRef, (snapshot) => {
      this.zone.run(() => callback(snapshot.val()));
    });
  }

  // -- RTDB: Streams (user-scoped) --

  onStream(
    uid: string,
    sessionId: string,
    callback: (data: StreamData | null) => void,
  ): Unsubscribe {
    const dbRef = ref(this.db, `users/${uid}/streams/${sessionId}`);
    return onValue(dbRef, (snapshot) => {
      this.zone.run(() => callback(snapshot.val()));
    });
  }

  async clearStream(uid: string, sessionId: string): Promise<void> {
    const dbRef = ref(this.db, `users/${uid}/streams/${sessionId}`);
    await remove(dbRef);
  }

  // -- RTDB: Commands (user-scoped) --

  onCommandResult(
    uid: string,
    instanceId: string,
    commandId: string,
    callback: (cmd: Command) => void,
  ): Unsubscribe {
    const dbRef = ref(this.db, `users/${uid}/commands/${instanceId}/${commandId}`);
    return onValue(dbRef, (snapshot) => {
      const data = snapshot.val();
      if (data) {
        this.zone.run(() => callback(data));
      }
    });
  }

  async sendCommand(
    uid: string,
    instanceId: string,
    action: string,
    payload: unknown = {},
  ): Promise<string> {
    const user = this.currentUser;
    if (!user) throw new Error("Not authenticated");

    const commandsRef = ref(this.db, `users/${uid}/commands/${instanceId}`);
    const newRef = push(commandsRef);
    const commandId = newRef.key!;

    await set(newRef, {
      action,
      payload,
      status: "pending",
      user_id: user.uid,
      created_at: Date.now(),
      updated_at: Date.now(),
    });

    return commandId;
  }

  async sendCommandAndWait(
    uid: string,
    instanceId: string,
    action: string,
    payload: unknown = {},
  ): Promise<unknown> {
    const commandId = await this.sendCommand(uid, instanceId, action, payload);

    return new Promise((resolve, reject) => {
      const unsub = this.onCommandResult(uid, instanceId, commandId, (cmd) => {
        if (cmd.status === "done") {
          unsub();
          resolve(cmd.result);
        } else if (cmd.status === "error") {
          unsub();
          reject(new Error(cmd.error || "Command failed"));
        }
      });

      setTimeout(() => {
        unsub();
        reject(new Error("Command timeout"));
      }, 30000);
    });
  }

  // -- Firestore: Message History (user-scoped, nested under instances/sessions) --

  async getSessionHistory(
    uid: string,
    instanceId: string,
    sessionId: string,
  ): Promise<HistoryMessage[]> {
    const messagesRef = collection(
      this.firestore,
      "users",
      uid,
      "instances",
      instanceId,
      "sessions",
      sessionId,
      "messages",
    );
    const q = query(messagesRef, orderBy("created_at", "asc"));
    const snapshot = await getDocs(q);
    return snapshot.docs.map((d) => {
      const data = d.data();
      return {
        id: data.id || d.id,
        role: data.role || "user",
        content: data.content || "",
        tool_calls: data.tool_calls || [],
        created_at: data.created_at || "",
      } as HistoryMessage;
    });
  }
}

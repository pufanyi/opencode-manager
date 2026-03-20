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
  set,
  type Unsubscribe,
} from "firebase/database";
import {
  type Firestore,
  collection,
  getDocs,
  getFirestore,
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
}

export interface Presence {
  online: boolean;
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

  // ── Auth ──

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

  // ── Account Linking ──

  onUserLinkStatus(uid: string, callback: (isLinked: boolean) => void): Unsubscribe {
    const dbRef = ref(this.db, `users/${uid}/telegram_id`);
    return onValue(dbRef, (snapshot) => {
      this.zone.run(() => callback(snapshot.exists() && snapshot.val() !== null));
    });
  }

  async generateLinkCode(uid: string): Promise<string> {
    const code = Math.floor(100000 + Math.random() * 900000).toString(); // 6-digit code
    const dbRef = ref(this.db, `link_codes/${code}`);
    await set(dbRef, {
      uid,
      expires: Date.now() + 10 * 60 * 1000, // 10 minutes
    });
    return code;
  }

  // ── RTDB Listeners ──

  onInstances(callback: (instances: Instance[]) => void): Unsubscribe {
    const dbRef = ref(this.db, "instances");
    return onValue(dbRef, (snapshot) => {
      const data = snapshot.val();
      const instances: Instance[] = data ? Object.values(data) : [];
      instances.sort((a, b) => a.name.localeCompare(b.name));
      this.zone.run(() => callback(instances));
    });
  }

  onPresence(instanceId: string, callback: (presence: Presence | null) => void): Unsubscribe {
    const dbRef = ref(this.db, `presence/${instanceId}`);
    return onValue(dbRef, (snapshot) => {
      this.zone.run(() => callback(snapshot.val()));
    });
  }

  onStream(sessionId: string, callback: (data: StreamData | null) => void): Unsubscribe {
    const dbRef = ref(this.db, `streams/${sessionId}`);
    return onValue(dbRef, (snapshot) => {
      this.zone.run(() => callback(snapshot.val()));
    });
  }

  onCommandResult(
    instanceId: string,
    commandId: string,
    callback: (cmd: Command) => void,
  ): Unsubscribe {
    const dbRef = ref(this.db, `commands/${instanceId}/${commandId}`);
    return onValue(dbRef, (snapshot) => {
      const data = snapshot.val();
      if (data) {
        this.zone.run(() => callback(data));
      }
    });
  }

  // ── Commands (Web → Go) ──

  async sendCommand(instanceId: string, action: string, payload: unknown = {}): Promise<string> {
    const user = this.currentUser;
    if (!user) throw new Error("Not authenticated");

    const commandsRef = ref(this.db, `commands/${instanceId}`);
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
    instanceId: string,
    action: string,
    payload: unknown = {},
  ): Promise<unknown> {
    const commandId = await this.sendCommand(instanceId, action, payload);

    return new Promise((resolve, reject) => {
      const unsub = this.onCommandResult(instanceId, commandId, (cmd) => {
        if (cmd.status === "done") {
          unsub();
          resolve(cmd.result);
        } else if (cmd.status === "error") {
          unsub();
          reject(new Error(cmd.error || "Command failed"));
        }
      });

      // Timeout after 30 seconds.
      setTimeout(() => {
        unsub();
        reject(new Error("Command timeout"));
      }, 30000);
    });
  }

  // ── Firestore: Message History ──

  async getSessionHistory(sessionId: string): Promise<HistoryMessage[]> {
    const messagesRef = collection(this.firestore, "sessions", sessionId, "messages");
    const q = query(messagesRef, orderBy("created_at", "asc"));
    const snapshot = await getDocs(q);
    return snapshot.docs.map((doc) => {
      const data = doc.data();
      return {
        id: data["id"] || doc.id,
        role: data["role"] || "user",
        content: data["content"] || "",
        tool_calls: data["tool_calls"] || [],
        created_at: data["created_at"] || "",
      } as HistoryMessage;
    });
  }
}

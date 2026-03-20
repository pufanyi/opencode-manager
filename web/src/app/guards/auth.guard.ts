import { inject } from "@angular/core";
import { type CanActivateFn, Router } from "@angular/router";
import { filter, map, take } from "rxjs";
import { FirebaseService } from "../services/firebase.service";

export const authGuard: CanActivateFn = () => {
  const firebase = inject(FirebaseService);
  const router = inject(Router);

  return firebase.user$.pipe(
    filter((user) => user !== undefined),
    take(1),
    map((user) => {
      if (user) return true;
      return router.createUrlTree(["/login"]);
    }),
  );
};

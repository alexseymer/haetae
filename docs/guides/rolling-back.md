# Rolling back

A bad deploy should not take your shop down, and with Haetae it usually does not. This guide explains what is automatic and what you do by hand.

## What happens by itself

If the build fails, for example because of a typo in the code, Haetae stops there. The version that is running is never touched, because the replacement only starts once the build has succeeded. The deploy shows **Failed** and the error text.

If the build works but the new containers do not start or do not become healthy, Haetae restores the previous version automatically. The deploy shows **Rolled back** with the error of the new version and the note "The new version failed to start. The previous version was restored." Rebuilding the old version is quick, because Docker still has its layers. In the rare case that going back also fails, the status is **Rollback failed** and you need to look at the host.

A failed or rolled-back deploy also raises an alert, so you hear about it in chat.

## Going back by hand

Sometimes a version starts fine but is wrong, say `shop-web` shows an empty page. Open the project, find an earlier healthy deploy in the deploy history, press **Rollback** next to it, and confirm. Haetae starts a new deploy of that earlier version, and it appears in the history marked as a rollback.

## What is kept

Volumes, and so the data in `shop-db`, survive every rollback, failed deploy and project removal. Haetae never runs `compose down -v` and never deletes data. Removing a project stops its containers and keeps the volumes.

Note that rolling back restores the old program, not old data. If a new version changed the database layout, going back does not undo that.

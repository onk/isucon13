-- これは init.sh からは呼ばれない
alter table livestream_tags add index livestream_id (livestream_id);
alter table livestream_tags add index tag_id_and_livestream_id (tag_id, livestream_id desc);
alter table livestreams add index user_id (user_id);
alter table livecomments add index livestream_id_and_created_at (livestream_id, created_at desc);
alter table themes add index user_id (user_id);
alter table icons add index user_id (user_id);
alter table reservation_slots add index end_at (end_at);
alter table livestream_viewers_history add index livestream_id (livestream_id);
alter table livecomment_reports add index livestream_id (livestream_id);
alter table ng_words add index user_id_and_livestream_id_and_created_at (user_id, livestream_id, created_at desc);
alter table ng_words add index livestream_id (livestream_id);
alter table reactions add index livestream_id_and_created_at (livestream_id, created_at desc);


-- 初期はコレ -- プロフィール画像
-- CREATE TABLE `icons` (
--   `id` BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
--   `user_id` BIGINT NOT NULL,
--   `image` LONGBLOB NOT NULL
-- ) ENGINE=InnoDB CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
DROP TABLE icons;
CREATE TABLE `icons` (
`id` bigint NOT NULL AUTO_INCREMENT,
`user_id` bigint NOT NULL,
`hash` varchar(255) NOT NULL,
PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
alter table icons add index user_id (user_id);

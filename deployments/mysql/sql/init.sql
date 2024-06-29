---- drop ----
DROP TABLE IF EXISTS `users`;

---- create ----
create table IF not exists `users`
(
 `id`               INT(20) AUTO_INCREMENT,
 `name`             VARCHAR(20) NOT NULL,
 `request_num`	    INT(20) DEFAULT 0,
 `created_at`       Datetime DEFAULT CURRENT_TIMESTAMP,
 `updated_at`       Datetime DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`)
);
